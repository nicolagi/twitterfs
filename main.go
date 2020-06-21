package main

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kurrik/oauth1a"
	"github.com/kurrik/twittergo"
	"github.com/lionkov/go9p/p"
	"github.com/lionkov/go9p/p/srv"
)

var (
	Eoff     = &p.Error{Err: "invalid dir read offset", Errornum: p.EINVAL}
	Esmall   = &p.Error{Err: "too small read size for dir entry", Errornum: p.EINVAL}
	Eunknown = &p.Error{Err: "unknown command", Errornum: p.EINVAL}

	idStrExpr = regexp.MustCompile(`^[0-9]{8}[0-9]*$`)
)

func newEIO(err error) *p.Error {
	return &p.Error{
		Errornum: p.EIO,
		Err:      fmt.Sprintf("%+v", err), // Make the stacktrace visible.
	}
}

type fsOps struct {
	client *twittergo.Client
	root   *node

	// The below is only used to get the list of followed users.
	// In turn, that's used for populating the root directory.
	screenName string
}

func newFileSystemOps(client *twittergo.Client, screenName string) *fsOps {
	fs := new(fsOps)
	fs.client = client
	fs.screenName = screenName
	fs.root = (*node)(nil).addChild("root", 0555|p.DMDIR, rootKind)
	fs.root.dir.Mtime = uint32(time.Now().Unix())
	fs.root.dir.Atime = fs.root.dir.Mtime
	ctl := fs.root.addChild("ctl", 0220, controlKind)
	ctl.dir.Mtime = fs.root.dir.Mtime
	ctl.dir.Atime = fs.root.dir.Mtime
	return fs
}

func (fs *fsOps) Attach(r *srv.Req) {
	if r.Afid != nil {
		r.RespondError(srv.Enoauth)
	} else {
		r.Fid.Aux = fs.root
		r.RespondRattach(&fs.root.dir.Qid)
	}
}

func (fs *fsOps) ensureLoaded(n *node) error {
	if n.loaded {
		return nil
	}
	switch n.kind {
	case rootKind:
		followed, err := apiFriendsList(fs.client, fs.screenName)
		if err != nil {
			return err
		}
		for _, u := range followed {
			// The check is for when the loaded flag is reset to false via the control file.
			// We may already know about this user.
			if _, ok := n.children[u.ScreenName]; !ok {
				n.addUser(u)
			}
		}
		n.prepareDirEntries()
		n.loaded = true
	case userKind:
		timeline, err := apiStatusesUserTimeline(fs.client, n.dir.Name, n.batchSize, "", "")
		if err != nil {
			return err
		}
		n.addTimeline(timeline)
		n.loaded = true
	}
	return nil
}

func (fs *fsOps) Walk(r *srv.Req) {
	var walked []p.Qid
	n := r.Fid.Aux.(*node)
	for _, name := range r.Tc.Wname {
		if child, err := fs.walk1(n, name); (child == nil && err == nil) || err == srv.Enoent {
			break
		} else if err != nil {
			r.RespondError(err)
			return
		} else if child != nil {
			n = child
			walked = append(walked, n.dir.Qid)
		}
	}
	// Per walk(9p), an error should be returned
	// only if the very first name can't be walked.
	if len(walked) == 0 && len(r.Tc.Wname) > 0 {
		r.RespondError(srv.Enoent)
		return
	}
	r.Newfid.Aux = n
	r.RespondRwalk(walked)
}

func (fs *fsOps) walk1(parent *node, childName string) (child *node, err *p.Error) {
	if parent.kind != rootKind && parent.kind != userKind {
		return nil, srv.Enotdir.(*p.Error)
	}
	if err := fs.ensureLoaded(parent); err != nil {
		return nil, newEIO(err)
	}
	if parent.kind == userKind && childName == ".." {
		return fs.root, nil
	}
	if child, ok := parent.children[childName]; ok {
		return child, nil
	}
	if cerr, ok := parent.errors[childName]; ok {
		if time.Until(cerr.until) < 0 {
			return nil, cerr.err
		} else {
			delete(parent.errors, childName)
		}
	}
	if parent.kind == rootKind {
		if user, err := apiUsersShow(fs.client, childName); err != nil {
			return nil, parent.cacheErrorResponse(childName, err)
		} else {
			return parent.addUser(user), nil
		}
	}
	if !idStrExpr.MatchString(childName) {
		return nil, nil
	}
	if tweet, err := apiStatusesShow(fs.client, childName); err != nil {
		return nil, parent.cacheErrorResponse(childName, err)
	} else {
		return parent.addTweet(tweet), nil
	}
}

func (fs *fsOps) Open(r *srv.Req) {
	r.RespondRopen(&r.Fid.Aux.(*node).dir.Qid, 0)
}

func (fs *fsOps) Create(r *srv.Req) {
	r.RespondError(srv.Eperm)
}

func (fs *fsOps) Read(r *srv.Req) {
	n := r.Fid.Aux.(*node)
	if err := fs.ensureLoaded(n); err != nil {
		r.RespondError(newEIO(err))
		return
	}
	// All our files are small.
	offset := int(r.Tc.Offset)
	count := int(r.Tc.Count)
	switch n.kind {
	case rootKind, userKind:
		// The offset must be the end of one of the dir entries.
		if offset > 0 {
			i := sort.SearchInts(n.boundaries, offset)
			if i == len(n.boundaries) || n.boundaries[i] != offset {
				r.RespondError(Eoff)
				return
			}
		}
		// We can't return truncated entries, so we may have to decrease count.
		j := sort.SearchInts(n.boundaries, offset+count)
		if j == len(n.boundaries) || n.boundaries[j] != offset+count {
			if j == 0 {
				count = 0
			} else {
				count = n.boundaries[j-1] - offset
			}
		}
		if count < 0 {
			r.RespondError(Esmall)
			return
		}
		r.RespondRread(n.buffer[offset : offset+count])
	case tweetKind:
		if offset >= len(n.buffer) {
			r.RespondRread(nil)
		} else {
			b := n.buffer[offset:]
			if count >= len(b) {
				r.RespondRread(b)
			} else {
				r.RespondRread(b[:count])
			}
		}
	default:
		r.RespondError(srv.Eperm)
	}
}

func (fs *fsOps) Write(r *srv.Req) {
	ctl := r.Fid.Aux.(*node)
	if ctl.kind != controlKind {
		r.RespondError(srv.Eperm)
		return
	}
	n := ctl.parent
	var cmd string
	var args []string
	if fields := strings.Fields(string(r.Tc.Data[:r.Tc.Count])); len(fields) > 1 {
		cmd = fields[0]
		args = fields[1:]
	} else if len(fields) > 0 {
		cmd = fields[0]
	}
	if n.kind == rootKind && cmd == "reload" {
		n.loaded = false
		r.RespondRwrite(r.Tc.Count)
	} else if (n.kind == rootKind || n.kind == userKind) && cmd == "batch" && len(args) == 1 {
		size, err := strconv.Atoi(args[0])
		if err != nil {
			r.RespondError(newEIO(err))
		} else {
			n.setBatchSize(size)
			r.RespondRwrite(r.Tc.Count)
		}
	} else if n.kind == userKind && cmd == "older" {
		timeline, err := apiStatusesUserTimeline(fs.client, n.dir.Name, n.batchSize, "", n.minID)
		if err != nil {
			r.RespondError(newEIO(err))
		} else {
			n.addTimeline(timeline)
			r.RespondRwrite(r.Tc.Count)
		}
	} else if n.kind == userKind && cmd == "newer" {
		timeline, err := apiStatusesUserTimeline(fs.client, n.dir.Name, n.batchSize, n.maxID, "")
		if err != nil {
			r.RespondError(newEIO(err))
		} else {
			n.addTimeline(timeline)
			r.RespondRwrite(r.Tc.Count)
		}
	} else {
		r.RespondError(Eunknown)
	}
}

func (fs *fsOps) Clunk(r *srv.Req) {
	r.RespondRclunk()
}

func (fs *fsOps) Remove(r *srv.Req) {
	r.RespondError(srv.Eperm)
}

func (fs *fsOps) Stat(r *srv.Req) {
	r.RespondRstat(&r.Fid.Aux.(*node).dir)
}

func (fs *fsOps) Wstat(r *srv.Req) {
	r.RespondError(srv.Eperm)
}

func newClient(c *fsConfig) *twittergo.Client {
	config := &oauth1a.ClientConfig{
		ConsumerKey:    c.APIKey,
		ConsumerSecret: c.APISecretKey,
	}
	user := oauth1a.NewAuthorizedConfig(c.AccessToken, c.AccessTokenSecret)
	return twittergo.NewClient(config, user)
}

func main() {
	c, err := loadDefaultConfig()
	if err != nil {
		log.Fatalf("%+v", err)
	}
	fs := newFileSystemOps(newClient(c), c.ScreenName)
	var s srv.Srv
	s.Dotu = false
	//s.Debuglevel = srv.DbgPrintFcalls
	s.Id = "twitter"
	s.Start(fs)
	if err := s.StartNetListener("tcp", "127.0.0.1:7731"); err != nil {
		log.Fatal(err)
	}
}

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
	"github.com/pkg/errors"
)

var (
	Eoff      = &p.Error{Err: "invalid dir read offset", Errornum: p.EINVAL}
	Esmall    = &p.Error{Err: "too small read size for dir entry", Errornum: p.EINVAL}
	Eunknown  = &p.Error{Err: "unknown command", Errornum: p.EINVAL}
	Eorphaned = &p.Error{Err: "node was orphaned", Errornum: p.EINVAL}

	// Let's find out right at start-up whether these type assertions fail.
	Enoauth *p.Error = srv.Enoauth.(*p.Error)
	Enotdir *p.Error = srv.Enotdir.(*p.Error)
	Eperm   *p.Error = srv.Eperm.(*p.Error)

	idStrExpr = regexp.MustCompile(`^[0-9]{8}[0-9]*$`)
)

func newEIO(err error) *p.Error {
	return &p.Error{
		Errornum: p.EIO,
		Err:      fmt.Sprintf("%+v", err), // Make the stacktrace visible.
	}
}

func respondError(r *srv.Req, err *p.Error) {
	log.Printf("%v — Rerror: %v", r.Tc, err)
	r.RespondError(err)
}

type fsOps struct {
	client *twittergo.Client
	root   *node

	//  The batch size determines how many tweets to load at a time for a user,
	// or for the home or mentions timelines.
	batchSize int
}

func newFileSystemOps(client *twittergo.Client, screenName string) *fsOps {
	fs := new(fsOps)
	fs.client = client
	fs.batchSize = 10
	fs.root = (*node)(nil).addChild("root", 0555|p.DMDIR, rootKind)
	fs.root.dir.Mtime = uint32(time.Now().Unix())
	fs.root.dir.Atime = fs.root.dir.Mtime
	ctl := fs.root.addChild("ctl", 0220, controlKind)
	ctl.dir.Mtime = fs.root.dir.Mtime
	ctl.dir.Atime = fs.root.dir.Mtime
	home := fs.root.addChild("home", 0555|p.DMDIR, homeKind)
	home.dir.Mtime = fs.root.dir.Mtime
	home.dir.Atime = fs.root.dir.Mtime
	mentions := fs.root.addChild("mentions", 0555|p.DMDIR, mentionsKind)
	mentions.dir.Mtime = fs.root.dir.Mtime
	mentions.dir.Atime = fs.root.dir.Mtime
	users := fs.root.addChild("users", 0555|p.DMDIR, usersKind)
	users.dir.Mtime = fs.root.dir.Mtime
	users.dir.Atime = fs.root.dir.Mtime
	fs.root.prepareDirEntries()
	fs.root.loaded = true
	return fs
}

func (fs *fsOps) Attach(r *srv.Req) {
	if r.Afid != nil {
		respondError(r, Enoauth)
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
	case homeKind:
		timeline, err := apiStatusesHomeTimeline(fs.client, fs.batchSize, "", "")
		if err != nil {
			return err
		}
		n.addTimeline(timeline)
		n.loaded = true
	case mentionsKind:
		timeline, err := apiStatusesMentionsTimeline(fs.client, fs.batchSize, "", "")
		if err != nil {
			return err
		}
		n.addTimeline(timeline)
		n.loaded = true
	case userKind:
		timeline, err := apiStatusesUserTimeline(fs.client, n.dir.Name, fs.batchSize, "", "")
		if err != nil {
			return err
		}
		n.addTimeline(timeline)
		n.loaded = true
	case usersKind:
		followed, err := apiFriendsList(fs.client)
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
	}
	return nil
}

func (fs *fsOps) Walk(r *srv.Req) {
	var walked []p.Qid
	n := r.Fid.Aux.(*node)
	if n.kind == orphanedKind {
		respondError(r, Eorphaned)
		return
	}
	for _, name := range r.Tc.Wname {
		if child, err := fs.walk1(n, name); (child == nil && err == nil) || err == srv.Enoent {
			break
		} else if err != nil {
			respondError(r, err)
			return
		} else if child != nil {
			n = child
			walked = append(walked, n.dir.Qid)
		}
	}
	// Per walk(9p), an error should be returned
	// only if the very first name can't be walked.
	if len(walked) == 0 && len(r.Tc.Wname) > 0 {
		respondError(r, srv.Enoent)
		return
	}
	r.Newfid.Aux = n
	r.RespondRwalk(walked)
}

func (fs *fsOps) walkdd(parent *node) (child *node, err *p.Error) {
	switch parent.kind {
	case homeKind, mentionsKind, usersKind:
		return fs.root, nil
	case userKind:
		return fs.root.children["users"], nil
	default:
		log.Printf("fixme: walkdd() for node of kind %v", parent.kind)
		return nil, srv.Enoent
	}
}

func (fs *fsOps) walk1(parent *node, childName string) (child *node, err *p.Error) {
	if parent.dir.Mode&p.DMDIR == 0 {
		return nil, Enotdir
	}
	if err := fs.ensureLoaded(parent); err != nil {
		return nil, newEIO(err)
	}
	if childName == ".." {
		return fs.walkdd(parent)
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
	if parent.kind == usersKind {
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
	n := r.Fid.Aux.(*node)
	if n.kind == orphanedKind {
		respondError(r, Eorphaned)
		return
	}
	r.RespondRopen(&n.dir.Qid, 0)
}

func (fs *fsOps) Create(r *srv.Req) {
	respondError(r, Eperm)
}

func (fs *fsOps) Read(r *srv.Req) {
	n := r.Fid.Aux.(*node)
	if n.kind == orphanedKind {
		respondError(r, Eorphaned)
		return
	}
	if err := fs.ensureLoaded(n); err != nil {
		respondError(r, newEIO(err))
		return
	}
	// All our files are small.
	offset := int(r.Tc.Offset)
	count := int(r.Tc.Count)
	switch n.kind {
	case homeKind, mentionsKind, userKind, usersKind, rootKind:
		// The offset must be the end of one of the dir entries.
		if offset > 0 {
			i := sort.SearchInts(n.boundaries, offset)
			if i == len(n.boundaries) || n.boundaries[i] != offset {
				respondError(r, Eoff)
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
			respondError(r, Esmall)
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
		respondError(r, Eperm)
	}
}

func (fs *fsOps) Write(r *srv.Req) {
	ctl := r.Fid.Aux.(*node)
	if ctl.kind != controlKind {
		respondError(r, Eperm)
		return
	}
	var cmd string
	var args []string
	if fields := strings.Fields(string(r.Tc.Data[:r.Tc.Count])); len(fields) > 1 {
		cmd = fields[0]
		args = fields[1:]
	} else if len(fields) > 0 {
		cmd = fields[0]
	}
	if cmd == "reply" && len(args) > 1 {
		idStr := args[0]
		// Don't use args, just strip the "post" and the separator.
		if err := apiStatusesUpdate(fs.client, string(r.Tc.Data[6+len(idStr):]), idStr); err != nil {
			respondError(r, newEIO(err))
		}
		r.RespondRwrite(r.Tc.Count)
	} else if cmd == "post" && len(args) > 0 {
		// Don't use args, just strip the "post" and the separator.
		if err := apiStatusesUpdate(fs.client, string(r.Tc.Data[5:]), ""); err != nil {
			respondError(r, newEIO(err))
		}
		r.RespondRwrite(r.Tc.Count)
	} else if cmd == "reload" {
		fs.root.loaded = false
		r.RespondRwrite(r.Tc.Count)
	} else if cmd == "batch" && len(args) == 1 {
		size, err := strconv.Atoi(args[0])
		if err != nil {
			respondError(r, newEIO(err))
		} else {
			fs.batchSize = size
			r.RespondRwrite(r.Tc.Count)
		}
	} else if cmd == "older" && len(args) == 1 {
		var dest *node
		if args[0][0] == '@' {
			dest = fs.root.children["users"].children[args[0][1:]]
		} else if args[0] == "home" {
			dest = fs.root.children["home"]
		} else if args[0] == "mentions" {
			dest = fs.root.children["mentions"]
		}
		if dest == nil {
			// In particular of args[0] does not start with '@' and is not "mentions".
			respondError(r, newEIO(srv.Enoent))
			return
		}
		var timeline twittergo.Timeline
		var err error
		if args[0][0] == '@' {
			timeline, err = apiStatusesUserTimeline(fs.client, dest.dir.Name, fs.batchSize, "", dest.minID)
		} else if args[0] == "home" {
			timeline, err = apiStatusesHomeTimeline(fs.client, fs.batchSize, "", dest.minID)
		} else if args[0] == "mentions" {
			timeline, err = apiStatusesMentionsTimeline(fs.client, fs.batchSize, "", dest.minID)
		}
		if err != nil {
			respondError(r, newEIO(err))
			return
		}
		dest.addTimeline(timeline)
		r.RespondRwrite(r.Tc.Count)
	} else if cmd == "newer" && len(args) == 1 {
		var dest *node
		if args[0][0] == '@' {
			dest = fs.root.children["users"].children[args[0][1:]]
		} else if args[0] == "home" {
			dest = fs.root.children["home"]
		} else if args[0] == "mentions" {
			dest = fs.root.children["mentions"]
		}
		if dest == nil {
			// In particular of args[0] does not start with '@' and is not "mentions".
			respondError(r, newEIO(srv.Enoent))
			return
		}
		var timeline twittergo.Timeline
		var err error
		if args[0][0] == '@' {
			timeline, err = apiStatusesUserTimeline(fs.client, dest.dir.Name, fs.batchSize, dest.maxID, "")
		} else if args[0] == "home" {
			timeline, err = apiStatusesHomeTimeline(fs.client, fs.batchSize, dest.maxID, "")
		} else if args[0] == "mentions" {
			timeline, err = apiStatusesMentionsTimeline(fs.client, fs.batchSize, dest.maxID, "")
		}
		if err != nil {
			respondError(r, newEIO(err))
			return
		}
		dest.addTimeline(timeline)
		r.RespondRwrite(r.Tc.Count)
	} else if cmd == "trim" && len(args) == 2 {
		desiredLength, err := strconv.Atoi(args[1])
		if err != nil {
			respondError(r, newEIO(errors.Errorf("%q: %v", args[1], err)))
			return
		}
		if desiredLength < 0 {
			respondError(r, newEIO(errors.Errorf("%q: can't trim to negative size", args[1])))
			return
		}
		var dest *node
		if args[0][0] == '@' {
			dest = fs.root.children["users"].children[args[0][1:]]
		} else if args[0] == "home" {
			dest = fs.root.children["home"]
		} else if args[0] == "mentions" {
			dest = fs.root.children["mentions"]
		}
		if dest == nil {
			// In particular of args[0] does not start with '@' and is not "mentions".
			respondError(r, newEIO(srv.Enoent))
			return
		}
		dest.trim(desiredLength)
		r.RespondRwrite(r.Tc.Count)
	} else {
		respondError(r, Eunknown)
	}
}

func (fs *fsOps) Clunk(r *srv.Req) {
	r.RespondRclunk()
}

func (fs *fsOps) Remove(r *srv.Req) {
	respondError(r, Eperm)
}

func (fs *fsOps) Stat(r *srv.Req) {
	n := r.Fid.Aux.(*node)
	if n.kind == orphanedKind {
		respondError(r, Eorphaned)
		return
	}
	r.RespondRstat(&n.dir)
}

func (fs *fsOps) Wstat(r *srv.Req) {
	respondError(r, Eperm)
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
	if err := s.StartNetListener("tcp", c.ListenAddress); err != nil {
		log.Fatal(err)
	}
}

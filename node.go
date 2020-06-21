package main

import (
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/kurrik/twittergo"
	"github.com/lionkov/go9p/p"
	"github.com/lionkov/go9p/p/srv"
	"github.com/pkg/errors"
)

var (
	owner string
	group string

	lastQIDPath uint64
)

func nextQIDPath() uint64 {
	return atomic.AddUint64(&lastQIDPath, 1)
}

func init() {
	if runtime.GOOS == "plan9" {
		owner = os.Getenv("user")
		group = owner
	} else {
		owner = p.OsUsers.Uid2User(os.Getuid()).Name()
		group = p.OsUsers.Gid2Group(os.Getgid()).Name()
	}
}

type nodeKind int

const (
	rootKind nodeKind = iota
	userKind
	tweetKind
	controlKind
)

type cachedErr struct {
	until time.Time
	err   *p.Error
}

type node struct {
	// For all nodes.
	kind nodeKind
	dir  p.Dir

	// For the control node to know what node it controls (a user
	// directory or the root directory).
	parent *node

	// For directory nodes, i.e., root and user nodes.
	children map[string]*node

	// For root and user nodes. Has the initial list of followed user
	// been loaded? Has the initial list of tweets been loaded?
	loaded bool

	// Serialized directory entries for root and user nodes, formatted
	// tweet for tweet nodes.
	buffer []byte

	// Directory entry boundaries, for root and user nodes (directory nodes).
	boundaries []int

	// For user nodes to know the range of loaded tweets, and know what
	// to do if requested to load older or newer tweets. The batch size
	// is also a property of the root, so it can be changed at once for
	// all users.
	minID     string
	maxID     string
	batchSize int

	// For root and user nodes. Caches error API responses. Shells do
	// all sorts of lookups and we don't want to call Twitter for those.
	// At least, not too often. (In addition, we avoid calling APIs with
	// non-numeric ids for tweets.)
	errors map[string]cachedErr
}

func isNotFound(err error) bool {
	switch e := err.(type) {
	case twittergo.ResponseError:
		return e.Code == http.StatusNotFound
	case twittergo.Errors:
		for _, inner := range e.Errors() {
			switch inner.Code() {
			// https://developer.twitter.com/en/docs/basics/response-codes
			case 17, 34, 50, 144, 421, 422:
				return true
			}
		}
		return false
	default:
		return false
	}
}

func (n *node) cacheErrorResponse(childName string, err error) *p.Error {
	cause := errors.Cause(err)
	var cerr cachedErr
	if isNotFound(cause) {
		cerr.until = time.Now().Add(time.Hour)
		cerr.err = srv.Enoent
	} else if e, ok := cause.(twittergo.RateLimitError); ok {
		cerr.until = e.RateLimitReset()
		cerr.err = newEIO(err)
	} else {
		cerr.until = time.Now().Add(5 * time.Minute)
		cerr.err = newEIO(err)
	}
	n.errors[childName] = cerr
	return cerr.err
}

func (n *node) addChild(name string, mode uint32, kind nodeKind) *node {
	child := new(node)
	if n != nil {
		child.parent = n
		n.children[name] = child
		child.batchSize = n.batchSize
	} else {
		child.batchSize = 10
	}
	child.kind = kind
	child.dir.Name = name
	child.dir.Uid = owner
	child.dir.Gid = group
	child.dir.Mode = mode
	if mode&p.DMDIR != 0 {
		child.children = make(map[string]*node)
		child.errors = make(map[string]cachedErr)
		child.dir.Qid.Type = p.QTDIR
	}
	child.dir.Qid.Path = nextQIDPath()
	return child
}

func (n *node) addUser(u twitterUser) *node {
	child := n.addChild(u.ScreenName, 0555|p.DMDIR, userKind)
	child.dir.Mtime = u.Mtime()
	child.dir.Atime = child.dir.Mtime
	ctl := child.addChild("ctl", 0222, controlKind)
	ctl.dir.Mtime = child.dir.Mtime
	ctl.dir.Atime = child.dir.Mtime
	return child
}

func (n *node) addTweet(tweet twittergo.Tweet) *node {
	child := n.addChild(tweet.IdStr(), 0444, tweetKind)
	child.buffer = formatTweet(n.dir.Name, tweet)
	child.dir.Length = uint64(len(child.buffer))
	child.dir.Mtime = uint32(tweet.CreatedAt().Unix())
	child.dir.Atime = child.dir.Mtime
	return child
}
func (n *node) addTimeline(timeline twittergo.Timeline) {
	for _, tweet := range timeline {
		idStr := tweet.IdStr()
		if n.minID == "" || n.minID > idStr {
			n.minID = idStr
		}
		if n.maxID == "" || n.maxID < idStr {
			n.maxID = idStr
		}
		// The check is for when the loaded flag is reset to false via the control file.
		// We may already know about this tweet.
		if _, ok := n.children[idStr]; !ok {
			n.addTweet(tweet)
		}
	}
	n.prepareDirEntries()
}

func (n *node) prepareDirEntries() {
	n.buffer = nil
	n.boundaries = nil
	end := 0
	for _, child := range n.children {
		dent := p.PackDir(&child.dir, false)
		n.buffer = append(n.buffer, dent...)
		end += len(dent)
		n.boundaries = append(n.boundaries, end)
	}
}

func (n *node) setBatchSize(size int) {
	if n.kind != rootKind && n.kind != userKind {
		// Tempted to panic: invariant violation.
		return
	}
	n.batchSize = size
	if n.kind == rootKind {
		for _, child := range n.children {
			// Not for control file.
			if child.kind == userKind {
				child.batchSize = size
			}
		}
	}
}

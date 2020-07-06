package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sort"
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
	controlKind  nodeKind = iota // /ctl — the control node for sending commands
	homeKind                     // /home — the home timeline, a listing of tweets
	mentionsKind                 // /mentions — the tweets that mentioned the authenticated user
	orphanedKind                 // a tweet that's been trimmed — not linked into the fs
	rootKind                     // / — the root
	tweetKind                    // /mentions/1234 or /users/janet/1234 or /home/1234 — a tweet
	userKind                     // /users/janet — @janet's timeline
	usersKind                    // /users — user listing, lazily loaded, starting from followed users
)

func (k nodeKind) String() string {
	switch k {
	case controlKind:
		return "control"
	case homeKind:
		return "home-timeline"
	case mentionsKind:
		return "mentions-timeline"
	case orphanedKind:
		return "orphaned"
	case rootKind:
		return "root"
	case tweetKind:
		return "tweet"
	case userKind:
		return "user-timeline"
	case usersKind:
		return "users"
	default:
		return fmt.Sprintf("nodeKind%d", k)
	}
}

type cachedErr struct {
	until time.Time
	err   *p.Error
}

type node struct {
	// For all nodes.
	kind nodeKind
	dir  p.Dir

	// For directory nodes, i.e., root node, home node, mentions node,
	// users node, and user timeline nodes.
	children map[string]*node

	// For directory nodes that need to call Twitter APIs, i.e., all
	// timeline nodes, and the users node. Caches error API responses.
	// Shells do all sorts of lookups and we don't want to call Twitter
	// for those. At least, not too often. (In addition, we avoid
	// calling APIs with non-numeric ids for tweets.)
	errors map[string]cachedErr

	// For lazily loaded directory nodes (users node, timeline nodes).
	// Has the initial list of followed user been loaded? Has the
	// initial list of tweets been loaded?
	loaded bool

	// Serialized directory entries for directory nodes;
	// formatted tweet for tweet nodes.
	buffer []byte

	// Directory entry boundaries, for directory nodes.
	boundaries []int

	// For timeline nodes to know the range of loaded tweets, and know
	// what to do if requested to load older or newer tweets.
	minID string
	maxID string
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
	if n != nil && n.dir.Mode&p.DMDIR == 0 {
		log.Printf("fixme: addChild() called for node of kind %v which is not a directory", n.kind)
		return nil
	}
	child := new(node)
	if n != nil {
		n.children[name] = child
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
	if n.kind != usersKind {
		log.Printf("fixme: addUser() called for node of kind %v", n.kind)
		return nil
	}
	child := n.addChild(u.ScreenName, 0555|p.DMDIR, userKind)
	child.dir.Mtime = u.Mtime()
	child.dir.Atime = child.dir.Mtime
	return child
}

func (n *node) addTweet(tweet twittergo.Tweet) *node {
	if n.kind != homeKind && n.kind != mentionsKind && n.kind != userKind {
		log.Printf("fixme: addTweet() called for node of kind %v", n.kind)
		return nil
	}
	child := n.addChild(tweet.IdStr(), 0444, tweetKind)
	child.buffer = formatTweet(n.dir.Name, tweet)
	child.dir.Length = uint64(len(child.buffer))
	child.dir.Mtime = uint32(tweet.CreatedAt().Unix())
	child.dir.Atime = child.dir.Mtime
	return child
}

func (n *node) addTimeline(timeline twittergo.Timeline) {
	if n.kind != homeKind && n.kind != mentionsKind && n.kind != userKind {
		log.Printf("fixme: addTimeline() called for node of kind: %v", n.kind)
		return
	}
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

type byModified []*node

func (nodes byModified) Len() int { return len(nodes) }

func (nodes byModified) Less(a, b int) bool { return nodes[a].dir.Mtime > nodes[b].dir.Mtime }

func (nodes byModified) Swap(a, b int) { nodes[a], nodes[b] = nodes[b], nodes[a] }

func (n *node) trim(size int) {
	if n.kind != homeKind && n.kind != mentionsKind && n.kind != userKind {
		log.Printf("fixme: trim() called for node of kind: %v", n.kind)
		return
	}
	if len(n.children) <= size {
		return
	}
	if size == 0 {
		n.children = make(map[string]*node)
		n.minID = ""
		n.maxID = ""
		n.prepareDirEntries()
		return
	}
	var tweets []*node
	for _, tweet := range n.children {
		tweets = append(tweets, tweet)
	}
	sort.Sort(byModified(tweets))
	n.minID = tweets[size-1].dir.Name
	for i := size; i < len(tweets); i++ {
		tweets[i].kind = orphanedKind
		delete(n.children, tweets[i].dir.Name)
	}
	n.prepareDirEntries()
}

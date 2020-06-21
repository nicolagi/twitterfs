package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kurrik/twittergo"
)

// Try to convert tweet URLs to relative paths (from a user directory).
// Identity on non-tweet URLs.
func localizeURL(currentUser, url string) string {
	local := url
	if !strings.HasPrefix(local, "https://twitter.com/") {
		return url
	}
	local = local[20:]
	i := strings.IndexByte(local, '/')
	if i == -1 {
		return url
	}
	user := strings.ToLower(local[:i])
	local = local[i:]
	if !strings.HasPrefix(local, "/status/") {
		return url
	}
	idStr := local[8:]
	if len(idStr) == 0 {
		return url
	}
	for i := 0; i < len(idStr); i++ {
		if idStr[i] < '0' || idStr[i] > '9' {
			return url
		}
	}
	if currentUser == user {
		return idStr
	}
	return filepath.Join("..", user, idStr)
}

// This crappy type is just an exercise to write collectURLs only once.
type either struct {
	urls  []twittergo.URL
	media []twittergo.Media
}

func (e either) length() int { return len(e.urls) + len(e.media) }

func (e either) get(index int, fieldName string) interface{} {
	if e.urls != nil {
		return e.urls[index][fieldName]
	}
	return e.media[index][fieldName]
}

func collectURLs(set map[string]struct{}, values either, fieldName string) {
	for a := 0; a < values.length(); a++ {
		if b := values.get(a, fieldName); b != nil {
			if c, ok := b.(string); ok {
				set[c] = struct{}{}
			}
		}
	}
}

// The formatting of the tweet text is quite tentative and subject to change.
func formatTweet(currentUser string, tweet twittergo.Tweet) []byte {
	var text bytes.Buffer
	content := tweet.FullText()
	if content == "" {
		content = tweet.Text()
	}
	_, _ = fmt.Fprintf(
		&text,
		"@%s — %s — %s\n",
		currentUser,
		tweet.CreatedAt().Format(time.RFC3339),
		content,
	)
	if tweetPath := parentRelativePath(currentUser, tweet); tweetPath != "" {
		_, _ = fmt.Fprintf(&text, "Parent: %s\n", tweetPath)
	}
	if tweetPath := retweetedRelativePath(currentUser, tweet); tweetPath != "" {
		_, _ = fmt.Fprintf(&text, "Retweets: %s\n", tweetPath)
	}
	// Collect URLs from various parts of the Tweet JSON.
	urlSet := make(map[string]struct{})
	collectURLs(urlSet, either{urls: tweet.Entities().URLs()}, "expanded_url")
	collectURLs(urlSet, either{urls: tweet.ExtendedEntities().URLs()}, "expanded_url")
	collectURLs(urlSet, either{media: tweet.Entities().Media()}, "media_url_https")
	collectURLs(urlSet, either{media: tweet.ExtendedEntities().Media()}, "media_url_https")
	var urlList []string
	for url := range urlSet {
		urlList = append(urlList, url)
	}
	sort.Strings(urlList)
	for _, url := range urlList {
		_, _ = fmt.Fprintf(&text, "Link: %s\n", localizeURL(currentUser, url))
	}
	return text.Bytes()
}

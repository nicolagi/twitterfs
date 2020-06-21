package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kurrik/twittergo"
	"github.com/pkg/errors"
)

type twitterUser struct {
	ScreenName string `json:"screen_name"`
	CreatedAt  string `json:"created_at"`
}

func (u twitterUser) Mtime() uint32 {
	t, _ := time.Parse(time.RubyDate, u.CreatedAt)
	return uint32(t.Unix())
}

func apiUsersShow(client *twittergo.Client, screenName string) (twitterUser, error) {
	const path = "/1.1/users/show.json"
	params := url.Values{}
	params.Set("screen_name", screenName)
	request, err := http.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	if err != nil {
		return twitterUser{}, errors.WithStack(err)
	}
	response, err := client.SendRequest(request)
	if err != nil {
		return twitterUser{}, errors.WithStack(err)
	}
	var user twitterUser
	if err := response.Parse(&user); err != nil {
		return twitterUser{}, errors.WithStack(err)
	}
	user.ScreenName = strings.ToLower(user.ScreenName)
	return user, nil
}

func apiStatusesShow(client *twittergo.Client, idStr string) (twittergo.Tweet, error) {
	const path = "https://api.twitter.com/1.1/statuses/show.json"
	params := url.Values{}
	params.Set("id", idStr)
	request, err := http.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	response, err := client.SendRequest(request)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var tweet twittergo.Tweet
	if err := response.Parse(&tweet); err != nil {
		return nil, errors.WithStack(err)
	}
	return tweet, nil
}

func apiFriendsList(client *twittergo.Client, followerScreenName string) ([]twitterUser, error) {
	const path = "/1.1/friends/list.json"
	params := url.Values{}
	params.Set("count", "200")
	params.Set("skip_status", "true")
	params.Set("include_user_entities", "false")
	params.Set("screen_name", followerScreenName)
	var users []twitterUser
more:
	request, err := http.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	response, err := client.SendRequest(request)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var obj struct {
		Users         []twitterUser `json:"users"`
		NextCursorStr string        `json:"next_cursor_str"`
	}
	if err := response.Parse(&obj); err != nil {
		return nil, errors.WithStack(err)
	}
	for _, user := range obj.Users {
		user.ScreenName = strings.ToLower(user.ScreenName)
		users = append(users, user)
	}
	if obj.NextCursorStr != "0" {
		params.Set("cursor", obj.NextCursorStr)
		goto more
	}
	return users, nil
}

func apiStatusesUserTimeline(client *twittergo.Client, screenName string, batchSize int, sinceID string, maxID string) (twittergo.Timeline, error) {
	const path = "/1.1/statuses/user_timeline.json"
	params := url.Values{}
	params.Set("tweet_mode", "extended")
	params.Set("screen_name", screenName)
	if sinceID != "" {
		params.Set("since_id", sinceID)
	}
	if maxID != "" {
		params.Set("max_id", maxID)
		batchSize++
	}
	params.Set("count", strconv.Itoa(batchSize))
	request, err := http.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	response, err := client.SendRequest(request)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var timeline twittergo.Timeline
	if err := response.Parse(&timeline); err != nil {
		return nil, errors.WithStack(err)
	}
	return timeline, nil
}

func get(tweet twittergo.Tweet, fieldName string) (fieldValue string, ok bool) {
	if val := tweet[fieldName]; val != nil {
		if str, ok := val.(string); ok {
			return str, true
		}
	}
	return "", false
}

func parentRelativePath(currentUser string, tweet twittergo.Tweet) string {
	if screenName, ok := get(tweet, "in_reply_to_screen_name"); ok {
		screenName = strings.ToLower(screenName)
		if idStr, ok := get(tweet, "in_reply_to_status_id_str"); ok {
			if currentUser != screenName {
				return filepath.Join("..", screenName, idStr)
			} else {
				return idStr
			}
		}
	}
	return ""
}

func tweetRelativePath(currentUser string, tweet twittergo.Tweet) string {
	if screenName := strings.ToLower(tweet.User().ScreenName()); screenName != "" {
		if idStr := tweet.IdStr(); idStr != "" {
			if currentUser != screenName {
				return filepath.Join("..", screenName, idStr)
			} else {
				return idStr
			}
		}
	}
	return ""
}

func retweetedRelativePath(currentUser string, tweet twittergo.Tweet) string {
	if val := tweet["retweeted_status"]; val != nil {
		if tweet, ok := val.(map[string]interface{}); ok {
			return tweetRelativePath(currentUser, twittergo.Tweet(tweet))
		}
	}
	return ""
}

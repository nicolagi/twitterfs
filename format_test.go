package main

import (
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
)

func TestLocalizeURL(t *testing.T) {
	t.Run("happy paths", func(t *testing.T) {
		testCases := []struct {
			user string
			url  string
			path string
		}{
			{"netbsdsrc", "https://twitter.com/netbsdsrc/status/1271800794002710540", "1271800794002710540"},
			{"npr", "https://twitter.com/netbsdsrc/status/1271800794002710540", "../netbsdsrc/1271800794002710540"},
			{"netbsdsrc", "https://twitter.com/NPR/status/1274574891338129409", "../npr/1274574891338129409"},
			{"npr", "https://twitter.com/NPR/status/1274574891338129409", "1274574891338129409"},
			{"npr", "https://twitter.com/DLangille/status/1267451982383656961", "../dlangille/1267451982383656961"},
		}
		for _, tc := range testCases {
			if got, want := localizeURL(tc.user, tc.url), tc.path; got != want {
				t.Errorf("got %v, want %v", got, want)
			}
		}
	})
	t.Run("unhappy paths by mutating one byte", func(t *testing.T) {
		const url = "https://twitter.com/netbsdsrc/status/1271800794002710540"
		const urlLen = len(url)
		f := func(i int) bool {
			mut := []byte(url)
			mut[i] = '_'
			return string(mut) == localizeURL("npr", string(mut))
		}
		if err := quick.Check(f, &quick.Config{
			Values: func(values []reflect.Value, rand *rand.Rand) {
				for i := 0; i < len(values); i++ {
					v := rand.Intn(urlLen)
					// Don't mutate screen name or tweet id.
					for v >= 20 && v <= 28 {
						v = rand.Intn(urlLen)
					}
					values[i] = reflect.ValueOf(v)
				}
			},
		}); err != nil {
			t.Error(err)
		}
	})
	t.Run("manually entered unhappy paths", func(t *testing.T) {
		urls := []string{
			"https://twitter.com",
			"https://twitter.com/",
			"https://twitter.com/status/",
			"https://twitter.com/status/1234",
			"https://twitter.com/me/status",
			"https://twitter.com/me/status/",
		}
		for _, url := range urls {
			if got, want := localizeURL("me", url), url; got != want {
				t.Errorf("got %v, want %v", got, want)
			}
		}
	})
}

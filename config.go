package main

import (
	"encoding/json"
	"log"
	"os"
	"os/user"
	"path/filepath"

	"github.com/pkg/errors"
)

type fsConfig struct {
	APIKey            string `json:"api_key"`
	APISecretKey      string `json:"api_secret_key"`
	AccessToken       string `json:"access_token"`
	AccessTokenSecret string `json:"access_token_secret"`
	ScreenName        string `json:"screen_name"`
	ListenAddress     string `json:"listen_address"`
}

func loadDefaultConfig() (*fsConfig, error) {
	cuser, err := user.Current()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	f, err := os.Open(filepath.Join(cuser.HomeDir, "lib", "twitterfs", "config"))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("Warning, could not close %q: %v", f.Name(), err)
		}
	}()
	var config fsConfig
	if err := json.NewDecoder(f).Decode(&config); err != nil {
		return nil, errors.WithStack(err)
	}
	if config.ListenAddress == "" {
		config.ListenAddress = "localhost:7731"
	}
	return &config, nil
}

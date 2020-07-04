/*
Command twitterfs is a read-only 9P interface to Twitter.

§ 1. Configuration

Twitterfs is configured via $HOME/lib/twitterfs/config like so:

	; cat $home/lib/twitterfs/config
	{
		"api_key": "redacted",
		"api_secret_key": "redacted",
		"access_token": "redacted",
		"access_token_secret": "redacted",
		"screen_name": "your_screen_name",
		"listen_address": "localhost:7731"
	}

The keys/tokens/secrets can be obtained by creating a Twitter
application at https://developer.twitter.com/.

The screen name is your screen name, used to fetch the list of
followed users, to add to the root directory; see below.

§ 2. File system structure and operation

The server listens by default on 127.0.0.1:7731, also known as
tcp!127.0.0.1!7731.

The root directory contains a control file named ctl, and a
directory per user (screen name, to lower case).

At start-up, the root directory will contain only the followed users.
Walking into a user directory adds it to the file-system (if the user
exists).

Each user directory contains one file per tweet, named by its id.
Upon first listing, the user directory will contain the latest 10
tweets. Walking to a tweet file adds it to the file-system.

It is not permitted to create or remove files or directories, nor to
change their metadata such as their modification times or their
names.

The file-system is read-only, except control files, which are
write-only.

To load more tweets for a user,

    echo newer user >>ctl
    echo older user >>ctl

The tweets batch size is 10 by default. The command

    echo batch 50 >>ctl

will change the batch size to 50.

To reload the root directory (the list of followed users), use

    echo reload >>ctl

This will not remove unfollowed users, only add new followed users.
*/
package main

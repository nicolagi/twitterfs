# twitterfs

A read-only (for now) 9P interface to Twitter.

## Configuration

Configured via `$home/lib/twitterfs/config` like so:

    ; cat $home/lib/twitterfs/config
    {
    	"api_key": "redacted",
    	"api_secret_key": "redacted",
    	"access_token": "redacted",
    	"access_token_secret": "redacted",
    	"screen_name": "your_screen_name"
    }

The keys/tokens/secrets can be obtained by [creating a Twitter application](https://developer.twitter.com/).

The screen name is your screen name, used to fetch the list of
followed users, to add to the root directory; see below.

## File system structure and operation

The server listens on 127.0.0.1:7731, also known as tcp!127.0.0.1!7731.

The root directory contains a control file named "ctl", and a
directory per user (screen name, to lower case).

At start-up, the root directory will contain only the followed users.
Walking into a user directory adds it to the file-system (if the user
exists).

Each user directory contains one file per tweet, named by its id, and
one control file named "ctl". Upon first listing, the user directory
will contain the latest 10 tweets. Walking to a tweet file adds it to
the file-system.

It is not permitted to create or remove files or directories, nor to
change their metadata such as their modification times or their
names.

The file-system is read-only, except control files, which are
write-only.

To load more tweets for a user,

    echo newer >>user/ctl
    echo older >>user/ctl
    
The tweets batch size is 10 by default. To change the batch size for
a user,

    echo batch 50 >>user/ctl

or

    echo batch 50 >>ctl
    
to change it for all current and future user directories.

To reload the root directory (the list of followed users), use

    echo reload >>ctl

## Next steps

Implement following and unfollowing users.

Helper scripts to operate the file-system from acme.

## Someday/Maybe

Do not tie the file-system to one account? Could be interesting if it
remains read-only. The only tie to a user account right now is the
initial list of users in the root directory.

Design and implement posting tweets.

Design and implement posting replies.

Design and implement re-tweeting.


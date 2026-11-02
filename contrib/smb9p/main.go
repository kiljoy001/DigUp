// smb9p serves an SMB share as a read-only 9P2000 filesystem.
//
// The office data is on SMB; the indexer, Plan 9 and Harvey speak 9P.  This
// is the adapter: one process holds the service account, exposes the share
// as a namespace, and nothing is ever written back.
//
//	smb9p -server files.example.com:445 -share documents \
//	      -user 'DOMAIN\svc-index' -pass-env SMBSECRET
//
// A 9P client then mounts tcp!host!5640 and reads it like any other tree.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	var (
		addr    = flag.String("a", "tcp!127.0.0.1!5640", "9P address, net!host!port")
		smbHost = flag.String("server", "", "SMB server, host:port (445 for a normal share)")
		share   = flag.String("share", "", "share name")
		user    = flag.String("user", "", "user name (DOMAIN\\name or name@domain)")
		pass    = flag.String("pass", "", "password (prefer -pass-env)")
		passEnv = flag.String("pass-env", "", "environment variable holding the password")
		domain  = flag.String("domain", "", "NTLM domain, if not part of -user")
		root    = flag.String("root", "", "subdirectory of the share to expose as /")
	)
	flag.Parse()

	if *smbHost == "" || *share == "" || *user == "" {
		fmt.Fprintln(os.Stderr, "smb9p: -server, -share and -user are required")
		flag.Usage()
		os.Exit(2)
	}
	pw := *pass
	if *passEnv != "" {
		pw = os.Getenv(*passEnv)
		if pw == "" {
			logf("smb9p: %s is empty", *passEnv)
			os.Exit(2)
		}
	}

	b, err := dialSMB(*smbHost, *share, *user, pw, *domain)
	if err != nil {
		logf("smb: %v", err)
		os.Exit(1)
	}
	defer b.close()

	rootNode, err := b.root(*root)
	if err != nil {
		logf("smb: %s: %v", *root, err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		if err := (&server{root: rootNode}).listen(*addr); err != nil {
			logf("9p: %v", err)
			sig <- syscall.SIGTERM
		}
	}()

	logf("sharing %s/%s read-only", *smbHost, *share)
	<-sig
}

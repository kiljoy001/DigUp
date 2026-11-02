# smb9p — an SMB share as a read-only 9P filesystem

Office data lives on SMB. The indexer, Plan 9 and Harvey speak 9P. This is
the adapter between them: one process holds the service account, exposes one
share as a 9P2000 tree, and never writes to it.

```
smb9p -server files.example.com:445 -share documents \
      -user 'DOMAIN\svc-index' -pass-env SMBSECRET \
      -a 'tcp!127.0.0.1!5640'
```

Then, from any 9P client:

```
mount -a tcp!files.example.com!5640 /n/office     # Plan 9
mount -t 9p -o trans=tcp,port=5640,version=9p2000 host /mnt/office   # Linux
```

and for digfs:

```
mount office tcp!files.example.com!5640 /documents
sync office
```

Hits from there read `office:/lease.md`; the file's bytes are read through
the bridge and mirrored into digfs's cache, never written back.

## Flags

| flag | meaning |
|---|---|
| `-server host:port` | SMB server, `445` for a normal share |
| `-share name` | share to expose |
| `-user name` | `DOMAIN\name` or `name@domain` |
| `-pass` / `-pass-env` | password, or the environment variable holding it |
| `-domain` | NTLM domain, when it is not part of `-user` |
| `-root path` | expose a subdirectory of the share as `/` |
| `-a net!host!port` | where to serve 9P (default loopback) |

## What a client sees

- The share root (or `-root`) as `/`, directories `0555`, files `0444`.
- Real sizes and modification times from SMB; names as the server gives
  them, matched case-insensitively on walk (SMB is case-insensitive, 9P is
  not).
- Reads at any offset; the file is opened on the first read of a fid and
  closed at clunk.
- Writes, creates, removes and wstat are refused with `permission denied`.

## Limits worth knowing

- **One share per process.** Run one per share; they are cheap.
- **SMB2/3 only**, NTLM authentication. No SMB1, no Kerberos yet (the
  library has the hooks; this does not wire them up).
- **No per-user permissions.** The 9P side has no authentication, so anyone
  who can reach the port sees what the service account sees. Bind it to
  loopback or a trusted segment, one bridge per access boundary.
- **No symlinks** (SMB reparse points are not exposed).
- The process holds the credentials; `-pass-env` keeps them out of `ps`.

## Testing without a domain

`impacket`'s `smbserver.py` is an adequate SMB2 test server and needs no
root:

```
python3 -m venv /tmp/imp && /tmp/imp/bin/pip install impacket
/tmp/imp/bin/python /tmp/imp/bin/smbserver.py -smb2support -port 4455 \
    -username alice -password secret -readonly docs /tmp/share

./smb9p -server 127.0.0.1:4455 -share docs -user alice -pass secret
9p -a 'tcp!127.0.0.1!5640' ls /
```

One quirk that shaped the code: impacket reports end-of-file as an SMB error
whose packet the Go client cannot parse. The bridge decides end-of-file by
the size it has, not by the error, so a fully read file ends cleanly against
both impacket and Samba.

## Licence

No third-party code is copied; the SMB client is
[go-smb2](https://github.com/hirochachacha/go-smb2) (MIT). The 9P server is
the same wire code as `contrib/digfs`, reduced to read-only.

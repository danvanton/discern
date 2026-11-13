# Third-party files

| File | What | Version | Source |
|---|---|---|---|
| `makemkv-signing-key.gpg` | MakeMKV's release signing key, `MakeMKV (signature) <support@makemkv.com>`, DSA 2048, created 2017-05-26 | fingerprint `2ECF23305F1FC0B32001673394E3083A18042697` | `https://keyserver.ubuntu.com/pks/lookup?op=get&options=mr&search=0x2ECF23305F1FC0B32001673394E3083A18042697`, converted to binary with `gpg --dearmor` |
| `makemkv-sha.txt` | MakeMKV's clearsigned SHA-256 list for one release, unchanged | 2.0.0 | `https://www.makemkv.com/download/makemkv-sha-2.0.0.txt` |

## How the key was checked (2026-11-13)

MakeMKV publishes no fingerprint, so the key was cross-checked instead:

1. The key ID in the signature of `makemkv-sha-2.0.0.txt` is this fingerprint (`gpg --list-packets`).
2. keys.openpgp.org (`/vks/v1/by-fingerprint/2ECF23305F1FC0B32001673394E3083A18042697`) returns the same key. It strips the user ID, which it hasn't verified.
3. The key verifies the signatures of both `makemkv-sha-2.0.0.txt` (signed 2026-09-21) and `makemkv-sha-1.18.4.txt` (signed 2026-06-15). A changed copy of the 2.0.0 file fails.

SHA-256 of the files as committed:

- `makemkv-signing-key.gpg`: `5ab51c8ed0f6cd758a244ecf67ed2b49ec70fc3a53b56873c62b8b6d0a93db7f`
- `makemkv-sha.txt` (2.0.0): `593435abbf3cdb94599125f003a9f91b2fb7efa25508b35c7d79d1653b7ac1d4`

`scripts/update-makemkv-pin.sh` replaces `makemkv-sha.txt` when the pin moves to a new release. Update its version, source and hash above in the same pull request. The file's real check is its signature: `TestPinMatchesSignedFile` verifies it with the key on every CI run.

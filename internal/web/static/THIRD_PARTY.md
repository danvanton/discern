# Third-party files

| File | What | Version | Source | Licence |
|---|---|---|---|---|
| `htmx.min.js` | [htmx](https://htmx.org) | 2.0.4 | `dist/htmx.min.js` from the npm package `htmx.org@2.0.4` | Zero-Clause BSD |

How `htmx.min.js` was obtained, so it can be checked or updated the same way:

1. Download the package tarball from `https://registry.npmjs.org/htmx.org/-/htmx.org-2.0.4.tgz`.
2. Check it against the registry's published integrity value, `sha512-HLxMCdfXDOJirs3vBZl/ZLoY+c7PfM4Ahr2Ad4YXh6d22T5ltbTXFFkpx9Tgb2vvmWFMbIc3LqN2ToNkZJvyYQ==` (from `https://registry.npmjs.org/htmx.org/2.0.4`, field `dist.integrity`).
3. Extract `package/dist/htmx.min.js`.

SHA-256 of the file as committed: `e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447`.

The file is built into the binary with `embed`, so browsers load it from Trayarr itself, never from a CDN.

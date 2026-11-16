# TheDiscDB samples

A few disc records from [TheDiscDB](https://github.com/TheDiscDb/data), for the
`internal/discdb` tests: the same folder layout and files, with each title's
`Tracks` list removed (it isn't used, and it is most of the size). Taken from
commit `d27d3c9532da4a765b57e0a96739b5b102c2f8ad` on 2026-11-16.

- Alien (1979), 2013 Blu-ray: two `MainMovie` titles, one per cut.
- Star Trek II (1982) and III (1984), 2021 4K collection: the owner's own
  discs; `../makemkv/scan-star-trek-ii-uhd.json` and `-iii-uhd.json` are the
  owner's real scans of the UHD discs (jobs 5 and 7 on the homelab).
- Kill Bill Vol. 1 (2003), 2025 4K: the stall case behind the watchdog.
- Despicable Me (2010), 4K 2017: a fake-playlist disc.

SHA-256 of each file as downloaded from TheDiscDB, before `Tracks` was removed
(`data/movie/<path>` at the commit above):

| File | SHA-256 |
|---|---|
| `Alien (1979)/2013-blu-ray/disc01.json` | `6fb5846e08119901886c35c1063243394c6f02340736ee740404bbd07d093163` |
| `Alien (1979)/2013-blu-ray/release.json` | `760b89e098fa12101556e96ca6195e08517408a99012fa6bc1c401ffa82389fc` |
| `Alien (1979)/metadata.json` | `0a1cd881367748df8b47f6b182de332f6b464a591a1259410141b15e69d4504f` |
| `Despicable Me (2010)/4k-uhd--blu-ray-2017/disc01.json` | `7d75ee52c78856df4c8fd821af2fbb9731df3cef6d57364ed4472e275f79de7f` |
| `Despicable Me (2010)/4k-uhd--blu-ray-2017/disc02.json` | `7801408ac605981a08416ae7b65c99adf3832c9f3be12fc7556f64eaa4d3f0f1` |
| `Despicable Me (2010)/4k-uhd--blu-ray-2017/release.json` | `b1231a4a67e6986c309e0e7feaa970463d083aa4568842fb7d817646e59a1887` |
| `Despicable Me (2010)/metadata.json` | `748a7e78748bc7bc0d300a4556fda2c0d19aa45235aee9f10d82e6cc333747a5` |
| `Kill Bill Vol. 1 (2003)/2025-4k/disc01.json` | `4ceaa9c85ae1c1f580941a24bfa6bea354d85b72027a60fa78a7387c97fa84f5` |
| `Kill Bill Vol. 1 (2003)/2025-4k/disc02.json` | `a2ed1c34c28e92423fcacfbeecd02835c00200e62462ba2f72be398fea448e32` |
| `Kill Bill Vol. 1 (2003)/2025-4k/release.json` | `e1b9ce7b6ac556cfe159c57a2c2f50e17052683d3fa5f3c642e8283312a2bba8` |
| `Kill Bill Vol. 1 (2003)/metadata.json` | `ea31ad463bebaab306b42931e4819cf2eeb7b1ac8855b735f65328801cc785f3` |
| `Star Trek II The Wrath of Khan (1982)/2021-original-4-movie-collection-4k/disc01.json` | `6b9bb7eb7e18e3f8f4b2e59b2893994e80b0666be1265e01d6c362f99e8b4b7e` |
| `Star Trek II The Wrath of Khan (1982)/2021-original-4-movie-collection-4k/disc02.json` | `fc97dd36c000bb308d5d197f1b405dcb422e52c125f658eb55ad915a12ca1fb9` |
| `Star Trek II The Wrath of Khan (1982)/2021-original-4-movie-collection-4k/release.json` | `6d3b40b58b7f79018882dca2ef86ad26a9ba282a11d70790ab104fde9215484e` |
| `Star Trek II The Wrath of Khan (1982)/metadata.json` | `ee47e929571c9567fffef5341dbad1af7d43d8fa307f60aed11515067eb30b15` |
| `Star Trek III The Search for Spock (1984)/2021-original-4-movie-collection-4k/disc01.json` | `56e3f1b0a2d1a8a5b389006cb8168ecea7a4b9e79861c5d6942118d559e183d0` |
| `Star Trek III The Search for Spock (1984)/2021-original-4-movie-collection-4k/disc02.json` | `9a0ce967ae7bc99d92eda7a30c8e5deaedca7133cefeafc25e3e5d95d38fcc2b` |
| `Star Trek III The Search for Spock (1984)/2021-original-4-movie-collection-4k/release.json` | `b5d2357d6b3f3d42674837b6397b4de7276419af5317319620c6c0afe29680b7` |
| `Star Trek III The Search for Spock (1984)/metadata.json` | `bdd804e7f7d312fc9e4dfe0678aafce937785d822ed69d75d632e6a0b60b3754` |

TheDiscDB's data is under the MIT licence:

```
MIT License

Copyright (c) 2021 Luke Foust

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

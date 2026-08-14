# Media test corpus

Committed fixtures for the upload and processing suites. Regenerate with
`python3 generate_fixtures.py` only when the corpus itself changes — the files
are committed so the default test run needs no image toolchain.

Every hostile sample is a deliberately malformed container, not real malware.

## `valid/`

| File | Purpose |
|---|---|
| `landscape-2048x1152.jpg` | Larger than both output bounds; exercises resizing |
| `portrait-1200x1600.png` | Portrait orientation past both bounds |
| `small-64x64.png` | Smaller than both bounds; neither output may be upscaled |
| `exact-256x256.jpg` | Exactly the thumbnail bound; catches off-by-one resizing |
| `exact-1080x1080.png` | Exactly the web bound |
| `wide-4000x10.png` | Extreme aspect ratio; the short side must not round to zero |

## `boundary/`

| File | Purpose |
|---|---|
| `one-byte.bin` | The 1-byte lower size bound: admitted by size policy, rejected as an image |
| `one-pixel.png` | Smallest structurally valid image |

The oversized sample is **not committed**. A 50 MB blob costs every clone
forever to assert one comparison, so tests build it in a temp directory via
`build_oversized_png()` in `generate_fixtures.py`.

## `hostile/`

Each file targets one specific rejection:

| File | Must be rejected because |
|---|---|
| `jpeg-bytes-named.png` | Signature and declared type disagree |
| `png-trailing-payload.png`, `jpeg-trailing-payload.jpg` | Payload after the terminator |
| `png-concatenated.png`, `jpeg-concatenated.jpg` | Multi-image content |
| `png-truncated.png`, `jpeg-truncated.jpg` | Truncated mid-stream |
| `jpeg-no-eoi.jpg`, `png-no-iend.png` | Missing terminator |
| `png-bad-crc.png` | Chunk CRC does not match its payload |
| `png-dimension-bomb.png` | Header claims 60000×60000 from a tiny file |
| `png-animated.apng.png` | APNG animation control chunk |
| `not-an-image.png` | Not an image at all, named like one |
| `png-signature-only.png`, `jpeg-signature-only.jpg` | Valid signature, garbage body |

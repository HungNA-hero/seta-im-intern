#!/usr/bin/env python3
"""Regenerates the media test corpus.

Fixtures are committed so the default test run needs no image toolchain. Run
this only when the corpus itself changes:

    python3 services/asset-core/testdata/media/generate_fixtures.py

Every hostile sample is a deliberately malformed container, not real malware.
Each one targets a specific rejection the validators must make.
"""

import io
import os
import struct
import zlib

from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
VALID = os.path.join(HERE, "valid")
BOUNDARY = os.path.join(HERE, "boundary")
HOSTILE = os.path.join(HERE, "hostile")


def write(directory: str, name: str, payload: bytes) -> None:
    os.makedirs(directory, exist_ok=True)
    with open(os.path.join(directory, name), "wb") as handle:
        handle.write(payload)
    print(f"{os.path.relpath(os.path.join(directory, name), HERE)}: {len(payload)} bytes")


def encode(image: Image.Image, image_format: str, **options) -> bytes:
    buffer = io.BytesIO()
    image.save(buffer, format=image_format, **options)
    return buffer.getvalue()


def gradient(width: int, height: int) -> Image.Image:
    """Smooth two-axis ramp with two solid anchors.

    Smooth deliberately: a high-frequency pattern is nearly incompressible and
    made the 2048x1152 JPEG 1.1 MB on its own. This ramp encodes to ~99 KB and
    still exercises everything the suites assert — dimensions, aspect ratio,
    format preservation, and metadata stripping.
    """
    image = Image.new("RGB", (width, height))
    pixels = image.load()
    for x in range(width):
        for y in range(height):
            pixels[x, y] = (x * 255 // max(width - 1, 1), y * 255 // max(height - 1, 1), 128)

    # Solid blocks give resize output something unambiguous to compare against.
    for x in range(min(width, max(width // 8, 1))):
        for y in range(min(height, max(height // 8, 1))):
            pixels[x, y] = (255, 0, 0)
            pixels[width - 1 - x, height - 1 - y] = (0, 0, 255)
    return image


def png_chunk(chunk_type: bytes, payload: bytes) -> bytes:
    body = chunk_type + payload
    return struct.pack(">I", len(payload)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)


def build_valid() -> None:
    # Larger than both output bounding boxes, so resizing is exercised.
    write(VALID, "landscape-2048x1152.jpg", encode(gradient(2048, 1152), "JPEG", quality=90))
    write(VALID, "portrait-1200x1600.png", encode(gradient(1200, 1600), "PNG"))
    # Smaller than both bounds: neither output may be upscaled.
    write(VALID, "small-64x64.png", encode(gradient(64, 64), "PNG"))
    # Exactly one bound, to catch off-by-one resizing.
    write(VALID, "exact-256x256.jpg", encode(gradient(256, 256), "JPEG", quality=90))
    write(VALID, "exact-1080x1080.png", encode(gradient(1080, 1080), "PNG"))
    # Extreme aspect ratio: the short side must not round to zero.
    write(VALID, "wide-4000x10.png", encode(gradient(4000, 10), "PNG"))


def build_boundary() -> None:
    # The 1-byte lower bound is admitted by size policy and rejected as an image.
    write(BOUNDARY, "one-byte.bin", b"\xff")

    smallest = encode(Image.new("RGB", (1, 1), (255, 0, 0)), "PNG")
    write(BOUNDARY, "one-pixel.png", smallest)

    # The oversized boundary sample is deliberately NOT committed: a 50 MB blob
    # in git costs every clone forever to assert one comparison. Tests that need
    # it build it in a temp directory with build_oversized_png().


def build_oversized_png(size_bytes: int = 50_000_001) -> bytes:
    """Structurally valid PNG padded past the maximum, for size-rejection tests."""
    base = encode(gradient(512, 512), "PNG")
    body, iend = base[:-12], base[-12:]
    padding = b"x" * (size_bytes - len(base) - 16)
    return body + png_chunk(b"tEXt", b"pad\x00" + padding) + iend


def build_hostile() -> None:
    valid_png = encode(gradient(32, 32), "PNG")
    valid_jpeg = encode(gradient(32, 32), "JPEG")

    # Declared as PNG, actually JPEG bytes. Signature and declaration disagree.
    write(HOSTILE, "jpeg-bytes-named.png", valid_jpeg)

    # Trailing payload after the terminating chunk/marker.
    write(HOSTILE, "png-trailing-payload.png", valid_png + b"MZ\x90\x00" + b"A" * 512)
    write(HOSTILE, "jpeg-trailing-payload.jpg", valid_jpeg + b"<?php echo 1; ?>")

    # Two complete images concatenated: multi-image content is out of scope.
    write(HOSTILE, "png-concatenated.png", valid_png + valid_png)
    write(HOSTILE, "jpeg-concatenated.jpg", valid_jpeg + valid_jpeg)

    # Truncated before the terminator.
    write(HOSTILE, "png-truncated.png", valid_png[: len(valid_png) // 2])
    write(HOSTILE, "jpeg-truncated.jpg", valid_jpeg[: len(valid_jpeg) // 2])

    # Missing the terminating marker entirely.
    write(HOSTILE, "jpeg-no-eoi.jpg", valid_jpeg[:-2])
    write(HOSTILE, "png-no-iend.png", valid_png[:-12])

    # Corrupted IDAT CRC: chunk length is right, checksum is not.
    corrupted = bytearray(valid_png)
    corrupted[-13] ^= 0xFF
    write(HOSTILE, "png-bad-crc.png", bytes(corrupted))

    # Header claims dimensions far beyond the pixel-area limit while the file
    # stays tiny — the decompression-bomb shape.
    header = valid_png[:8]
    bomb = header + png_chunk(b"IHDR", struct.pack(">IIBBBBB", 60000, 60000, 8, 2, 0, 0, 0)) + valid_png[33:]
    write(HOSTILE, "png-dimension-bomb.png", bomb)

    # APNG animation control chunk: animated images are rejected.
    apng = valid_png[:33] + png_chunk(b"acTL", struct.pack(">II", 2, 0)) + valid_png[33:]
    write(HOSTILE, "png-animated.apng.png", apng)

    # Not an image at all, but named like one.
    write(HOSTILE, "not-an-image.png", b"#!/bin/sh\necho compromised\n")

    # Valid signature, garbage body.
    write(HOSTILE, "png-signature-only.png", valid_png[:8] + b"\x00" * 256)
    write(HOSTILE, "jpeg-signature-only.jpg", b"\xff\xd8\xff\xe0" + b"\x00" * 256)


if __name__ == "__main__":
    build_valid()
    build_boundary()
    build_hostile()

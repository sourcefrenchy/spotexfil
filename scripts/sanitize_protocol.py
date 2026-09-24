#!/usr/bin/env python3
"""sanitize_protocol.py - Rewrite protocol.json with per-seed values.

Used by `make obfuscated` so each obfuscated build ships unique crypto
labels and cover traffic instead of the stock IOCs embedded in
protocol.json. Deterministic given the same seed.

Usage: sanitize_protocol.py <protocol.json> <seed>
"""

import hashlib
import json
import random
import sys

# Pools of innocuous values for cover traffic. A seeded pick keeps each
# build's static indicators unique.
COVER_ADJ = [
    "Chill", "Morning", "Late Night", "Sunday", "Weekend", "Midnight",
    "Golden Hour", "Rainy Day", "Sunny", "Cozy", "Mellow", "Electric",
    "Acoustic", "Indie", "Lo-Fi", "Deep", "Smooth", "Easy", "Slow",
    "Happy", "Focus", "Dreamy", "Velvet", "Amber",
]
COVER_NOUN = [
    "Vibes", "Coffee", "Mix", "Tunes", "Playlist", "Groove", "Beats",
    "Session", "Sounds", "Tracks", "Mood", "Flow", "Drive", "Walk",
    "Study", "Workout", "Commute", "Kitchen", "Garden", "Porch",
]
FILLER_ARTISTS = [
    "Norah Jones", "Jack Johnson", "John Mayer", "Adele", "Ed Sheeran",
    "Coldplay", "Fleet Foxes", "Bon Iver", "Lana Del Rey", "Billie Eilish",
    "Dua Lipa", "The Weeknd", "Bruno Mars", "Sade", "Leon Bridges",
    "Khruangbin", "Men I Trust", "Tame Impala", "Glass Animals", "Hozier",
]


def label(seed: str, purpose: str) -> str:
    return hashlib.sha256(f"{purpose}-{seed}".encode()).hexdigest()[:16]


def main() -> None:
    path, seed = sys.argv[1], sys.argv[2]
    with open(path) as f:
        proto = json.load(f)

    rng = random.Random(seed)

    proto["c2"]["tag_label"] = label(seed, "tag")
    proto["c2"]["meta_key_label"] = label(seed, "meta")

    names = {f"{a} {n}" for a in rng.sample(COVER_ADJ, 12)
             for n in rng.sample(COVER_NOUN, 1)}
    proto["transport"]["cover_names"] = sorted(names)[:12]
    proto["transport"]["filler_artists"] = rng.sample(FILLER_ARTISTS, 7)

    with open(path, "w") as f:
        json.dump(proto, f, indent=2)
        f.write("\n")

    print(f"[*] Sanitized {path} (seed={seed})")
    print(f"    tag_label={proto['c2']['tag_label']} "
          f"meta_key_label={proto['c2']['meta_key_label']}")


if __name__ == "__main__":
    main()

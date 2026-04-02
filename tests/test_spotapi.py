"""Tests for spotapi.py - Spotify API wrapper with mocked API calls."""

import json
import os
import tempfile
from unittest.mock import MagicMock, patch, call

import pytest

# We need to mock environment variables before importing spotapi
MOCK_ENV = {
    "SPOTIFY_USERNAME": "testuser",
    "SPOTIFY_CLIENT_ID": "fake_client_id",
    "SPOTIFY_CLIENT_SECRET": "fake_client_secret",
    "SPOTIFY_REDIRECTURI": "http://localhost:8888/callback",
}


@pytest.fixture
def mock_env():
    """Set up mock environment variables."""
    with patch.dict(os.environ, MOCK_ENV):
        yield


@pytest.fixture
def mock_spotipy():
    """Create a mock spotipy.Spotify instance."""
    mock = MagicMock()
    mock.user_playlists.return_value = {'items': [], 'next': None}
    mock.search.return_value = {
        'artists': {
            'total': 1,
            'items': [{'id': 'artist123'}]
        }
    }
    mock.artist_top_tracks.return_value = {
        'tracks': [
            {'id': f'track{i}'} for i in range(5)
        ]
    }
    return mock


@pytest.fixture
def spot_instance(mock_env, mock_spotipy):
    """Create a Spot instance with legacy names (for backward-compat tests)."""
    with patch('spotapi.util.prompt_for_user_token', return_value='fake_token'):
        with patch('spotapi.spotipy.Spotify', return_value=mock_spotipy):
            from spotapi import Spot
            instance = Spot(use_cover_names=False)
            return instance


@pytest.fixture
def spot_cover(mock_env, mock_spotipy):
    """Create a Spot instance with cover names enabled."""
    with patch('spotapi.util.prompt_for_user_token', return_value='fake_token'):
        with patch('spotapi.spotipy.Spotify', return_value=mock_spotipy):
            from spotapi import Spot
            instance = Spot(use_cover_names=True)
            return instance


# --- Authentication Tests ---

class TestAuthentication:
    def test_missing_env_var_exits(self):
        """Missing environment variables cause sys.exit(1)."""
        from spotapi import Spot
        with patch.dict(os.environ, {}, clear=True):
            with patch('spotapi.load_config', return_value={}):
                with pytest.raises(SystemExit) as exc_info:
                    Spot()
                assert exc_info.value.code == 1

    def test_missing_single_var(self, mock_env):
        """Missing a single required var causes exit."""
        from spotapi import Spot
        env = dict(MOCK_ENV)
        del env["SPOTIFY_CLIENT_SECRET"]
        with patch.dict(os.environ, env, clear=True):
            with patch('spotapi.load_config', return_value={}):
                with pytest.raises(SystemExit) as exc_info:
                    Spot()
                assert exc_info.value.code == 1

    def test_successful_auth(self, mock_env, mock_spotipy):
        """Successful authentication creates spotipy client."""
        with patch('spotapi.util.prompt_for_user_token', return_value='tok'):
            with patch('spotapi.spotipy.Spotify', return_value=mock_spotipy):
                from spotapi import Spot
                s = Spot()
                assert s.spotipy == mock_spotipy
                assert s.username == "testuser"

    def test_null_token_exits(self, mock_env):
        """Null token causes exit."""
        with patch('spotapi.util.prompt_for_user_token', return_value=None):
            from spotapi import Spot
            with pytest.raises(SystemExit):
                Spot()


# --- Config File Tests ---

class TestConfigFile:
    def test_config_file_loads(self):
        """Config file provides credentials when env vars missing."""
        from spotapi import load_config
        with tempfile.NamedTemporaryFile(
            mode='w', suffix='.conf', delete=False
        ) as f:
            f.write("[spotify]\n")
            f.write("username = configuser\n")
            f.write("client_id = config_id\n")
            f.write("client_secret = config_secret\n")
            f.write("redirect_uri = http://localhost/cb\n")
            f.flush()
            path = f.name

        try:
            with patch('spotapi.CONFIG_PATHS', [path]):
                result = load_config()
            assert result['SPOTIFY_USERNAME'] == 'configuser'
            assert result['SPOTIFY_CLIENT_ID'] == 'config_id'
            assert result['SPOTIFY_CLIENT_SECRET'] == 'config_secret'
            assert result['SPOTIFY_REDIRECTURI'] == 'http://localhost/cb'
        finally:
            os.unlink(path)

    def test_config_file_missing_returns_empty(self):
        """Missing config file returns empty dict."""
        from spotapi import load_config
        with patch('spotapi.CONFIG_PATHS', ['/nonexistent/.conf']):
            result = load_config()
        assert result == {}

    def test_env_vars_override_config(self, mock_spotipy):
        """Environment variables take precedence over config."""
        from spotapi import Spot
        with tempfile.NamedTemporaryFile(
            mode='w', suffix='.conf', delete=False
        ) as f:
            f.write("[spotify]\n")
            f.write("username = config_user\n")
            f.write("client_id = config_id\n")
            f.write("client_secret = config_secret\n")
            f.write("redirect_uri = http://localhost/cb\n")
            f.flush()
            path = f.name

        try:
            with patch('spotapi.CONFIG_PATHS', [path]):
                with patch.dict(os.environ, MOCK_ENV):
                    with patch('spotapi.util.prompt_for_user_token',
                               return_value='tok'):
                        with patch('spotapi.spotipy.Spotify',
                                   return_value=mock_spotipy):
                            s = Spot()
            # Env vars should win
            assert s.username == 'testuser'
        finally:
            os.unlink(path)


# --- Playlist Pagination Tests ---

class TestGetAllPlaylists:
    def test_empty_account(self, spot_instance, mock_spotipy):
        """Empty account returns empty list."""
        mock_spotipy.user_playlists.return_value = {
            'items': [], 'next': None
        }
        result = spot_instance._get_all_playlists()
        assert result == []

    def test_single_page(self, spot_instance, mock_spotipy):
        """Single page of playlists."""
        playlists = [{'id': f'pl{i}', 'name': f'Playlist {i}'}
                     for i in range(10)]
        mock_spotipy.user_playlists.return_value = {
            'items': playlists, 'next': None
        }
        result = spot_instance._get_all_playlists()
        assert len(result) == 10

    def test_multiple_pages(self, spot_instance, mock_spotipy):
        """Multiple pages are fetched via pagination."""
        page1 = [{'id': f'pl{i}', 'name': f'P{i}'} for i in range(50)]
        page2 = [{'id': f'pl{i}', 'name': f'P{i}'} for i in range(50, 75)]

        mock_spotipy.user_playlists.side_effect = [
            {'items': page1, 'next': 'has_more'},
            {'items': page2, 'next': None},
        ]
        result = spot_instance._get_all_playlists()
        assert len(result) == 75


# --- Clear Data Tests ---

class TestClearData:
    def test_clears_legacy_playlists(self, spot_instance, mock_spotipy):
        """Legacy payloadChunk playlists are deleted."""
        mock_spotipy.user_playlists.return_value = {
            'items': [
                {'id': 'pl1', 'name': '1-payloadChunk'},
                {'id': 'pl2', 'name': '2-payloadChunk'},
                {'id': 'pl3', 'name': 'My Favorites'},
            ],
            'next': None,
        }
        # For non-legacy playlists, mock full fetch (no marker)
        mock_spotipy.user_playlist.return_value = {
            'description': 'just a normal playlist'
        }
        spot_instance.clear_data()

        calls = mock_spotipy.user_playlist_unfollow.call_args_list
        assert len(calls) == 2
        assert calls[0] == call('testuser', 'pl1')
        assert calls[1] == call('testuser', 'pl2')

    def test_clears_nothing_when_empty(self, spot_instance, mock_spotipy):
        """No unfollows when no payload playlists exist."""
        mock_spotipy.user_playlists.return_value = {
            'items': [{'id': 'pl1', 'name': 'My Music'}],
            'next': None,
        }
        mock_spotipy.user_playlist.return_value = {
            'description': 'normal description'
        }
        spot_instance.clear_data()
        mock_spotipy.user_playlist_unfollow.assert_not_called()

    def test_clears_cover_name_playlists(self, spot_cover, mock_spotipy):
        """Cover-named playlists with hidden markers are deleted."""
        from spotapi import MARKER_SEP
        mock_spotipy.user_playlists.return_value = {
            'items': [
                {'id': 'pl1', 'name': 'Chill Vibes #a3f2'},
                {'id': 'pl2', 'name': 'My Real Playlist'},
            ],
            'next': None,
        }
        mock_spotipy.user_playlist.side_effect = [
            {'description': f'chunkdata{MARKER_SEP}{{"i":1}}'},
            {'description': 'just music'},
        ]
        spot_cover.clear_data()

        calls = mock_spotipy.user_playlist_unfollow.call_args_list
        assert len(calls) == 1
        assert calls[0] == call('testuser', 'pl1')


# --- Retrieve Playlists Tests ---

class TestRetrievePlaylists:
    def test_retrieve_legacy_ordered(self, spot_instance, mock_spotipy):
        """Legacy playlists are reassembled in correct order."""
        mock_spotipy.user_playlists.return_value = {
            'items': [
                {'id': 'pl3', 'name': '3-payloadChunk'},
                {'id': 'pl1', 'name': '1-payloadChunk'},
                {'id': 'pl2', 'name': '2-payloadChunk'},
            ],
            'next': None,
        }
        mock_spotipy.user_playlist.side_effect = [
            {'description': 'chunk3', 'name': '3-payloadChunk'},
            {'description': 'chunk1', 'name': '1-payloadChunk'},
            {'description': 'chunk2', 'name': '2-payloadChunk'},
        ]
        result = spot_instance.retrieve_playlists()
        assert result == 'chunk1chunk2chunk3'

    def test_retrieve_skips_non_payload(self, spot_instance, mock_spotipy):
        """Non-payload playlists are ignored."""
        mock_spotipy.user_playlists.return_value = {
            'items': [
                {'id': 'pl1', 'name': '1-payloadChunk'},
                {'id': 'pl2', 'name': 'My Music'},
            ],
            'next': None,
        }
        mock_spotipy.user_playlist.side_effect = [
            {'description': 'data', 'name': '1-payloadChunk'},
            {'description': 'normal', 'name': 'My Music'},
        ]
        result = spot_instance.retrieve_playlists()
        assert result == 'data'

    def test_retrieve_empty(self, spot_instance, mock_spotipy):
        """No payload playlists returns empty string."""
        mock_spotipy.user_playlists.return_value = {
            'items': [], 'next': None
        }
        result = spot_instance.retrieve_playlists()
        assert result == ''

    def test_html_entities_unescaped(self, spot_instance, mock_spotipy):
        """HTML entities in descriptions are unescaped."""
        mock_spotipy.user_playlists.return_value = {
            'items': [{'id': 'pl1', 'name': '1-payloadChunk'}],
            'next': None,
        }
        mock_spotipy.user_playlist.return_value = {
            'description': 'data&amp;more',
            'name': '1-payloadChunk',
        }
        result = spot_instance.retrieve_playlists()
        assert result == 'data&more'

    def test_retrieve_cover_names_strips_metadata(
            self, spot_cover, mock_spotipy):
        """Cover-named playlists have metadata stripped on retrieval."""
        from spotapi import MARKER_SEP
        mock_spotipy.user_playlists.return_value = {
            'items': [
                {'id': 'pl1', 'name': 'Chill Vibes #a1b2'},
            ],
            'next': None,
        }
        mock_spotipy.user_playlist.return_value = {
            'description': f'payload_data{MARKER_SEP}{{"i":1}}',
            'name': 'Chill Vibes #a1b2',
        }
        result = spot_cover.retrieve_playlists()
        assert result == 'payload_data'


# --- Generate Playlists Tests ---

class TestGeneratePlaylists:
    def test_small_payload_single_playlist(
            self, spot_instance, mock_spotipy):
        """Small payload creates exactly one playlist (legacy mode)."""
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        spot_instance.generate_playlists("short payload")

        mock_spotipy.user_playlist_create.assert_called_once()
        args = mock_spotipy.user_playlist_create.call_args
        assert args[0][1] == '1-payloadChunk'

    def test_large_payload_multiple_playlists(
            self, spot_instance, mock_spotipy):
        """Payload > effective chunk size creates multiple playlists."""
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        payload = "A" * 1200
        spot_instance.generate_playlists(payload)

        assert mock_spotipy.user_playlist_create.call_count >= 3

    def test_payload_too_large_exits(self, spot_instance):
        """Payload over MAX_PAYLOAD_SIZE causes exit."""
        payload = "X" * 1_024_000
        with pytest.raises(SystemExit) as exc_info:
            spot_instance.generate_playlists(payload)
        assert exc_info.value.code == 1

    def test_filler_tracks_added(self, spot_instance, mock_spotipy):
        """Filler tracks are added to each playlist."""
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        spot_instance.generate_playlists("test")

        mock_spotipy.user_playlist_add_tracks.assert_called_once()
        track_ids = mock_spotipy.user_playlist_add_tracks.call_args[0][2]
        assert len(track_ids) == 5

    def test_cover_names_used(self, spot_cover, mock_spotipy):
        """Cover names mode generates innocuous playlist names."""
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        spot_cover.generate_playlists("test payload")

        args = mock_spotipy.user_playlist_create.call_args
        name = args[0][1]
        # Should NOT contain payloadChunk
        assert 'payloadChunk' not in name
        # Should contain # suffix
        assert '#' in name

    def test_cover_names_embed_metadata(self, spot_cover, mock_spotipy):
        """Cover names mode embeds index metadata in description."""
        from spotapi import MARKER_SEP
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        spot_cover.generate_playlists("test payload")

        args = mock_spotipy.user_playlist_create.call_args
        desc = args[1]['description']
        assert MARKER_SEP in desc
        meta_str = desc.split(MARKER_SEP, 1)[1]
        meta = json.loads(meta_str)
        assert meta['i'] == 1


# --- Filler Tracks Tests ---

class TestGetFillerTracks:
    def test_returns_track_ids(self, spot_instance, mock_spotipy):
        """Returns list of track ID strings."""
        tracks = spot_instance._get_filler_tracks()
        assert len(tracks) == 5
        assert all(isinstance(t, str) for t in tracks)

    def test_artist_not_found_returns_empty(
            self, spot_instance, mock_spotipy):
        """Unknown artist returns empty list."""
        mock_spotipy.search.return_value = {
            'artists': {'total': 0, 'items': []}
        }
        tracks = spot_instance._get_filler_tracks()
        assert tracks == []

    def test_custom_count(self, spot_instance, mock_spotipy):
        """Respects custom track count."""
        tracks = spot_instance._get_filler_tracks(count=3)
        assert len(tracks) == 3


# --- Cover Name Generation Tests ---

class TestCoverNames:
    def test_generate_cover_name_format(self, spot_cover):
        """Cover names have format 'Name #xxxx'."""
        name = spot_cover._generate_cover_name()
        assert '#' in name
        parts = name.rsplit('#', 1)
        assert len(parts[1]) == 4  # 4-char suffix

    def test_cover_names_are_random(self, spot_cover):
        """Multiple cover names are different."""
        names = {spot_cover._generate_cover_name() for _ in range(20)}
        assert len(names) > 5  # should be mostly unique

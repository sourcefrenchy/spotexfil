from mythic_container.MythicCommandBase import *
from mythic_container.MythicGoRPC import *


class ShellArguments(TaskArguments):
    def __init__(self, command_line, **kwargs):
        super().__init__(command_line, **kwargs)
        self.args = [
            CommandParameter(
                name="command",
                cli_name="command",
                display_name="Command",
                type=ParameterType.String,
                description="Shell command to execute on the target.",
                parameter_group_info=[ParameterGroupInfo(required=True, ui_position=0)],
            ),
        ]

    async def parse_arguments(self):
        if len(self.command_line.strip()) == 0:
            raise Exception("shell requires a command to run.\n\tUsage: {}".format(ShellCommand.help_cmd))
        if self.command_line.strip()[0] == "{":
            self.load_args_from_json_string(self.command_line)
        else:
            self.add_arg("command", self.command_line)


class ShellCommand(CommandBase):
    cmd = "shell"
    needs_admin = False
    help_cmd = "shell [command]"
    description = "Execute a single shell command on the target (sh -c on *nix, cmd /c on Windows)."
    version = 1
    author = "@sourcefrenchy"
    argument_class = ShellArguments
    attackmapping = ["T1059"]
    attributes = CommandAttributes(suggested_command=True)

    async def create_go_tasking(self, taskData: PTTaskMessageAllData) -> PTTaskCreateTaskingMessageResponse:
        response = PTTaskCreateTaskingMessageResponse(TaskID=taskData.Task.ID, Success=True)
        try:
            await SendMythicRPCArtifactCreate(MythicRPCArtifactCreateMessage(
                TaskID=taskData.Task.ID,
                BaseArtifactType="Process Create",
                ArtifactMessage=taskData.args.get_arg("command"),
            ))
        except Exception:
            pass  # artifact logging is best-effort only
        return response

    async def process_response(self, task: PTTaskMessageAllData, response: any) -> PTTaskProcessResponseMessageResponse:
        pass

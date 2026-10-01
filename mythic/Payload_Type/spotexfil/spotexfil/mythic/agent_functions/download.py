from mythic_container.MythicCommandBase import *
import json


class DownloadArguments(TaskArguments):
    def __init__(self, command_line, **kwargs):
        super().__init__(command_line, **kwargs)
        self.args = []

    async def parse_arguments(self):
        if len(self.command_line) == 0:
            raise Exception("Must provide the path of the file to download")
        try:
            # JSON means the task came from the file browser; flatten it to a
            # plain path string (what the agent expects as its tasking).
            tmp_json = json.loads(self.command_line)
            self.set_manual_args(tmp_json["path"] + "/" + tmp_json["file"])
        except Exception:
            # plain command-line path; pass it through untouched
            pass


class DownloadCommand(CommandBase):
    cmd = "download"
    needs_admin = False
    help_cmd = "download /remote/path/to/file"
    description = "Download a file from the target."
    version = 1
    supported_ui_features = ["file_browser:download"]
    author = "@sourcefrenchy"
    argument_class = DownloadArguments
    attackmapping = ["T1020", "T1030", "T1041"]

    async def create_go_tasking(self, taskData: PTTaskMessageAllData) -> PTTaskCreateTaskingMessageResponse:
        return PTTaskCreateTaskingMessageResponse(
            TaskID=taskData.Task.ID,
            Success=True,
            DisplayParams=taskData.args.get_command_line(),
        )

    async def process_response(self, task: PTTaskMessageAllData, response: any) -> PTTaskProcessResponseMessageResponse:
        pass

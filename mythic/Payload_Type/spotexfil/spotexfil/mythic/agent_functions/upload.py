from mythic_container.MythicCommandBase import *
from mythic_container.MythicGoRPC import *
import base64


class UploadArguments(TaskArguments):
    def __init__(self, command_line, **kwargs):
        super().__init__(command_line, **kwargs)
        self.args = [
            CommandParameter(
                name="file",
                cli_name="file",
                display_name="File to Upload",
                type=ParameterType.File,
                description="The local file to upload to the target.",
                parameter_group_info=[ParameterGroupInfo(required=True, ui_position=0)],
            ),
            CommandParameter(
                name="remote_path",
                cli_name="remote_path",
                display_name="Remote Path",
                type=ParameterType.String,
                description="Destination path on the target (defaults to the original filename).",
                parameter_group_info=[ParameterGroupInfo(required=False, ui_position=1)],
            ),
        ]

    async def parse_arguments(self):
        if len(self.command_line) == 0:
            raise Exception("upload requires a file and remote path")
        if self.command_line[0] == "{":
            self.load_args_from_json_string(self.command_line)
        else:
            raise Exception("upload requires the modal/JSON form: select a file and remote path")

    async def parse_dictionary(self, dictionary):
        self.load_args_from_dictionary(dictionary)


class UploadCommand(CommandBase):
    cmd = "upload"
    needs_admin = False
    help_cmd = "upload"
    description = "Upload a file to the target machine."
    version = 1
    supported_ui_features = ["file_browser:upload"]
    author = "@sourcefrenchy"
    argument_class = UploadArguments
    attackmapping = ["T1020", "T1030", "T1041", "T1105"]

    async def create_go_tasking(self, taskData: PTTaskMessageAllData) -> PTTaskCreateTaskingMessageResponse:
        response = PTTaskCreateTaskingMessageResponse(TaskID=taskData.Task.ID, Success=True)
        file_id = taskData.args.get_arg("file")
        remote_path = taskData.args.get_arg("remote_path") or ""

        # Resolve the original filename for display and default remote path.
        file_search = await SendMythicRPCFileSearch(MythicRPCFileSearchMessage(
            TaskID=taskData.Task.ID,
            AgentFileID=file_id,
            LimitByCallback=False,
            MaxResults=1,
        ))
        if not file_search.Success or len(file_search.Files) == 0:
            response.Success = False
            response.Error = f"Failed to find file {file_id} in Mythic: {file_search.Error}"
            return response
        original_file_name = file_search.Files[0].Filename

        if len(remote_path) == 0:
            remote_path = original_file_name
        elif remote_path[-1] == "/" or remote_path.endswith("\\"):
            remote_path = remote_path + original_file_name

        # Fetch the file bytes and stamp them into the tasking as
        # {"path": <remote path>, "content": <base64>} — the agent's contract.
        file_content = await SendMythicRPCFileGetContent(
            MythicRPCFileGetContentMessage(AgentFileID=file_id))
        if not file_content.Success:
            response.Success = False
            response.Error = f"Failed to fetch contents of {original_file_name}: {file_content.Error}"
            return response

        taskData.args.remove_arg("file")
        taskData.args.remove_arg("remote_path")
        taskData.args.add_arg("path", remote_path, ParameterType.String)
        taskData.args.add_arg("content", base64.b64encode(file_content.Content).decode(), ParameterType.String)

        response.DisplayParams = f"{original_file_name} to {remote_path}"
        return response

    async def process_response(self, task: PTTaskMessageAllData, response: any) -> PTTaskProcessResponseMessageResponse:
        pass

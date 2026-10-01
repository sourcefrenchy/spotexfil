from mythic_container.MythicCommandBase import *


class SysinfoArguments(TaskArguments):
    def __init__(self, command_line, **kwargs):
        super().__init__(command_line, **kwargs)
        self.args = []

    async def parse_arguments(self):
        pass


class SysinfoCommand(CommandBase):
    cmd = "sysinfo"
    needs_admin = False
    help_cmd = "sysinfo"
    description = "Collect system information from the target (hostname, OS, arch, user, network)."
    version = 1
    author = "@sourcefrenchy"
    argument_class = SysinfoArguments
    attackmapping = ["T1082", "T1033", "T1016"]
    attributes = CommandAttributes(suggested_command=True)

    async def create_go_tasking(self, taskData: PTTaskMessageAllData) -> PTTaskCreateTaskingMessageResponse:
        return PTTaskCreateTaskingMessageResponse(TaskID=taskData.Task.ID, Success=True)

    async def process_response(self, task: PTTaskMessageAllData, response: any) -> PTTaskProcessResponseMessageResponse:
        pass

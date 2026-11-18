const sessionID = "mock-session-long-content";
const turnID = "mock-turn-long-content";
const requestID = "mock-request-long-content";
const escapedSummary =
  "calibre 路径 &amp;#34;C:\\Books&amp;#34; 应显示为正常引号";
const escapedEnvironment =
  "&amp;lt;environment_context&amp;gt;mock cwd&amp;lt;/environment_context&amp;gt;";
const longUser =
  "You are an expert at upholding safety and compliance standards for Codex ambient suggestions. " +
  "用户要求：详情页必须能够读取完整命令，列表可以只显示摘要。" +
  "LONG-USER-BODY ".repeat(700) +
  "END-OF-COMPLETE-USER-COMMAND";
const longSystem = (
  "系统指令必须按需加载，并允许查看全文。LONG-SYSTEM-CONTEXT ".repeat(500)
).slice(0, 17704) + "END-OF-SYSTEM-INSTRUCTIONS";
const longAssistant =
  "**bold** <img src=x onerror=alert(1)>\n\n# Result\n*italic* and `inline`\n\n- first item\n- second item\n\n1. ordered item\n\n[Safe link](https://example.com/?q=a%26b) [Unsafe link](javascript:alert(1)) literal a%26b\n\n```js\nconst html = \"<script>alert(1)</script>\";\n```\n\n我会先说明处理结果，再展示必要的中间过程。" +
  "LONG-ASSISTANT-BODY ".repeat(280) +
  "END-OF-COMPLETE-ASSISTANT-ANSWER";
const longToolOutput =
  "命令执行结果：\n" +
  "TOOL-OUTPUT-LINE\n".repeat(7000) +
  "END-OF-LONG-TOOL-OUTPUT";


const originalRequest = {
  model: "gpt-5.6-sol",
  instructions: longSystem,
  input: [
    {
      role: "user",
      content: [{ type: "input_text", text: longUser }],
    },
  ],
  tools: [
    {
      type: "function",
      name: "shell_command",
      description: "Run a command with deliberately long mock data.",
    },
  ],
};
const originalResponse = {
  output: [
    {
      type: "function_call",
      call_id: "call-long-output",
      name: "shell_command",
      arguments: JSON.stringify({
        command: "printf a deliberately long command",
      }),
    },
    {
      type: "function_call_output",
      call_id: "call-long-output",
      output: longToolOutput,
    },
    {
      type: "message",
      role: "assistant",
      content: [{ type: "output_text", text: longAssistant }],
    },
  ],
};
const encode = (value) =>
  Buffer.from(JSON.stringify(value), "utf8").toString("base64");

originalRequest.input[0].content[0].text = escapedEnvironment+" "+longUser;
console.log(JSON.stringify({longUser,longAssistant,longSystem,longToolOutput,escapedSummary,escapedEnvironment,originalRequest,originalResponse}));

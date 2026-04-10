export function inputEventsToPrompt(events) {
  const lines = [];
  for (const event of events ?? []) {
    if (!event || typeof event !== 'object') {
      continue;
    }
    switch (event.type) {
      case 'user.message': {
        const text = extractText(event.content);
        if (text) {
          lines.push(text);
        }
        break;
      }
      case 'user.interrupt':
        lines.push('The user interrupted the previous task. Stop the prior plan and respond to the latest request.');
        break;
      case 'user.tool_confirmation':
        lines.push(`Tool confirmation for ${event.tool_use_id ?? 'unknown tool'}: ${JSON.stringify(event, null, 2)}`);
        break;
      case 'user.custom_tool_result':
        lines.push(`Custom tool result for ${event.custom_tool_use_id ?? 'unknown tool'}: ${JSON.stringify(event, null, 2)}`);
        break;
      default:
        lines.push(`Input event ${event.type}: ${JSON.stringify(event, null, 2)}`);
        break;
    }
  }
  return lines.filter(Boolean).join('\n\n');
}

function extractText(content) {
  if (typeof content === 'string') {
    return content.trim();
  }
  if (!Array.isArray(content)) {
    return '';
  }
  return content
    .filter((block) => block && block.type === 'text' && typeof block.text === 'string')
    .map((block) => block.text.trim())
    .filter(Boolean)
    .join('\n');
}

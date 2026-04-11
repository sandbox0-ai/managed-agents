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

export function inputEventsToSDKMessages(events) {
  const messages = [];
  let needsStructuredPrompt = false;
  for (const event of events ?? []) {
    if (!event || typeof event !== 'object') {
      continue;
    }
    if (event.type === 'user.message') {
      const content = normalizeMessageContent(event.content);
      if (content.length === 0) {
        continue;
      }
      if (content.some((block) => block.type !== 'text')) {
        needsStructuredPrompt = true;
      }
      messages.push(sdkUserMessage(content, event));
      continue;
    }
    const text = inputEventsToPrompt([event]);
    if (text) {
      messages.push(sdkUserMessage([{ type: 'text', text }], event));
    }
  }
  return needsStructuredPrompt ? asyncIterableFrom(messages) : null;
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

function normalizeMessageContent(content) {
  if (typeof content === 'string') {
    return content.trim() ? [{ type: 'text', text: content.trim() }] : [];
  }
  if (!Array.isArray(content)) {
    return [];
  }
  const out = [];
  for (const block of content) {
    if (!block || typeof block !== 'object') {
      continue;
    }
    if (block.type === 'text' && typeof block.text === 'string') {
      out.push({ type: 'text', text: block.text });
      continue;
    }
    if ((block.type === 'image' || block.type === 'document') && block.source && typeof block.source === 'object') {
      const mapped = {
        type: block.type,
        source: normalizeSource(block.source),
      };
      if (block.type === 'document') {
        if (typeof block.title === 'string' && block.title.trim() !== '') {
          mapped.title = block.title.trim();
        }
        if (typeof block.context === 'string' && block.context.trim() !== '') {
          mapped.context = block.context.trim();
        }
      }
      out.push(mapped);
    }
  }
  return out;
}

function normalizeSource(source) {
  const type = String(source.type ?? '').trim();
  switch (type) {
    case 'base64':
      return {
        type: 'base64',
        media_type: String(source.media_type ?? ''),
        data: String(source.data ?? ''),
      };
    case 'url':
      return {
        type: 'url',
        url: String(source.url ?? ''),
      };
    case 'text':
      return {
        type: 'text',
        media_type: String(source.media_type ?? 'text/plain'),
        data: String(source.data ?? ''),
      };
    default:
      return { ...source };
  }
}

function sdkUserMessage(content, event) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content,
    },
    parent_tool_use_id: null,
    timestamp: typeof event?.processed_at === 'string' ? event.processed_at : undefined,
  };
}

async function* asyncIterableFrom(items) {
  for (const item of items) {
    yield item;
  }
}

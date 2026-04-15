import { createServer } from './server.js';
import { logInfo } from './lib/log.js';

const port = Number(process.env.PORT ?? '8080');
const server = createServer();

server.listen(port, () => {
  logInfo('managed-agent wrapper listening', { port });
});

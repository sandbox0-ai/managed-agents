import { createServer } from './server.js';

const port = Number(process.env.PORT ?? '8080');
const server = createServer();

server.listen(port, () => {
  console.log(`managed-agent wrapper listening on :${port}`);
});

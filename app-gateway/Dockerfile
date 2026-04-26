FROM node:22-alpine

WORKDIR /app
COPY package*.json ./
RUN npm ci --omit=dev
COPY src ./src
COPY vendor ./vendor

ENV APP_GATEWAY_HTTP_ADDR=0.0.0.0:8787
EXPOSE 8787
CMD ["node", "src/server.js"]

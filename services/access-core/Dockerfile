FROM node:20-alpine AS dependencies
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY prisma ./prisma
RUN npm run db:generate

FROM dependencies AS dev
ENV NODE_ENV=development
COPY . .
EXPOSE 4000
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=5 \
  CMD wget -qO- http://127.0.0.1:4000/health >/dev/null || exit 1
CMD ["npm", "run", "dev"]

FROM dependencies AS build
COPY . .
RUN npm run build

FROM node:20-alpine AS prod
ENV NODE_ENV=production
WORKDIR /app
COPY --from=dependencies --chown=node:node /app/package.json /app/package-lock.json ./
COPY --from=dependencies --chown=node:node /app/node_modules ./node_modules
RUN npm prune --omit=dev && npm cache clean --force
COPY --from=build --chown=node:node /app/node_modules/.prisma ./node_modules/.prisma
COPY --from=build --chown=node:node /app/dist ./dist
USER node
EXPOSE 4000
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=5 \
  CMD wget -qO- http://127.0.0.1:4000/health >/dev/null || exit 1
CMD ["node", "dist/index.js"]

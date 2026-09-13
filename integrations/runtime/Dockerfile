FROM public.ecr.aws/lambda/nodejs:22 AS build

WORKDIR /build
COPY package.json package-lock.json tsconfig.json ./
COPY scripts ./scripts
RUN npm ci
COPY src ./src
RUN ./node_modules/.bin/tsc --outDir dist --declaration false
RUN npm prune --omit=dev

FROM public.ecr.aws/lambda/nodejs:22
COPY --from=build /build/package.json ${LAMBDA_TASK_ROOT}/package.json
COPY --from=build /build/node_modules ${LAMBDA_TASK_ROOT}/node_modules
COPY --from=build /build/dist ${LAMBDA_TASK_ROOT}/dist
CMD ["dist/handler.handler"]

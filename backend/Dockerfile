FROM golang:1.25 AS build
WORKDIR /app
COPY . .
RUN go build -o server .

FROM gcr.io/distroless/base-debian12
COPY --from=build /app/server /server
CMD ["/server"]

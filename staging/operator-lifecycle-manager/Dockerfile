FROM gcr.io/distroless/static:debug
LABEL stage=olm
WORKDIR /
# bundle unpack Jobs require cp at /bin/cp
RUN ["/busybox/ln", "-s", "/busybox/cp", "/bin/cp"]
# copy goreleaser built binaries
COPY olm /bin/olm
COPY catalog /bin/catalog
COPY package-server /bin/package-server
COPY copy-content /bin/copy-content
COPY cpb /bin/cpb
EXPOSE 8080
EXPOSE 5443
USER 1001
ENTRYPOINT ["/bin/olm"]

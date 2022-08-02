# ./pkg/controller/bundle/bundle_unpacker.go requires "/bin/cp"
FROM busybox
COPY olm catalog olm-auto-labeler package-server wait cpb /bin/
EXPOSE 8080
EXPOSE 5443
CMD ["/bin/olm"]

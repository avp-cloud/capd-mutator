FROM golang:1.19 as gobuild

# Set the Current Working Directory inside the container
WORKDIR $GOPATH/src/github.com/avp-cloud/eksa-capd-mutator

# Copy everything from the current directory to the PWD (Present Working Directory) inside the container
COPY . .

# Download all the dependencies
RUN go get -d -v ./...

# build the package
RUN go build -o /eksa-capd-mutator

FROM gcr.io/distroless/static-debian11

COPY --from=gobuild /eksa-capd-mutator /eksa-capd-mutator
# Run the executable
CMD ["/eksa-capd-mutator"]
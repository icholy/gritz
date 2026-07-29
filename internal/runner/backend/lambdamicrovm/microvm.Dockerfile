# Example Dockerfile for a Lambda MicroVMs image whose application is the gritz
# shim. This is the *container* layer of the image: it is packaged with the
# in-VM gritz binary into a zip, uploaded to S3, and built with
# `create-microvm-image --code-artifact uri=s3://…/app.zip --base-image-arn <al2023>
# --build-role-arn <role> --hooks '{"port":9000,...}'`. The resulting snapshot
# ARN is the image_identifier a workspace references. See README.md for the full
# recipe (S3 upload, base-image-arn, build-role IAM policy, per-hook timeouts).
#
# MicroVMs are ARM64-only, so FROM the arm64 container base and provide a
# linux/arm64 gritz binary. The `--base-image-arn` (…:aws:microvm-image:al2023-1)
# is the *separate* MicroVM OS base, NOT this container FROM.
FROM public.ecr.aws/lambda/microvms:al2023-minimal

# The driver binary must live at backend.BinaryPath. Build a linux/arm64 gritz
# binary and place it alongside this Dockerfile before zipping:
#   GOOS=linux GOARCH=arm64 go build -o gritz ./cmd/gritz
COPY gritz /usr/local/bin/gritz
RUN chmod 0755 /usr/local/bin/gritz

# The shim serves the AWS lifecycle + build hooks on the dedicated hook port 9000
# (awsmicrovm.HookPort — must match `--hooks port=9000`) and the gritz control
# surface (/gritz/lifecycle + /gritz/stop) on the ingress port 8080. Hook
# config is NOT baked here: hooks are declared at create-microvm-image time.
ENTRYPOINT ["/usr/local/bin/gritz", "tool", "microvm-shim"]

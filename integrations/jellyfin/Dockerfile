ARG DOTNET_VERSION=10.0
FROM mcr.microsoft.com/dotnet/sdk:${DOTNET_VERSION} AS build
ARG JELLYFIN_VERSION=12.0.0
ARG TARGET_FRAMEWORK=net10.0
WORKDIR /src
COPY JAVBeacon.Jellyfin/ ./JAVBeacon.Jellyfin/
RUN dotnet publish JAVBeacon.Jellyfin/JAVBeacon.Jellyfin.csproj -c Release -o /out -p:JellyfinVersion=${JELLYFIN_VERSION} -p:TargetFramework=${TARGET_FRAMEWORK} -p:TargetFrameworks=${TARGET_FRAMEWORK}

FROM scratch AS artifact
COPY --from=build /out/ /

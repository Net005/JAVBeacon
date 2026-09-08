using System.Globalization;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Entities.Movies;
using MediaBrowser.Controller.Providers;
using MediaBrowser.Model.Entities;
using MediaBrowser.Model.Providers;

namespace Jellyfin.Plugin.JAVBeacon.Providers;

public sealed class JAVBeaconImageProvider(JAVBeaconClient client) : IRemoteImageProvider
{
    public string Name => "JAVBeacon";
    public bool Supports(BaseItem item)
    {
        if (Plugin.Instance?.Configuration.EnableMetadata != true)
            return false;
        return item is Movie;
    }

    public IEnumerable<ImageType> GetSupportedImages(BaseItem item) => [ImageType.Primary, ImageType.Backdrop];

    public async Task<IEnumerable<RemoteImageInfo>> GetImages(BaseItem item, CancellationToken ct)
    {
        var id = FindReleaseId(item);
        if (!id.HasValue)
        {
            var match = await client.Match(item.Path, item.Name, ct).ConfigureAwait(false);
            if (!match?.Matched ?? true || match?.Release is null)
                return [];
            id = match.Release.ReleaseId;
            if (id <= 0)
                return [];
        }

        var dto = await client.Metadata(id.Value, ct).ConfigureAwait(false);
        if (dto is null) return [];
        var images = new List<RemoteImageInfo>();
        if (!string.IsNullOrWhiteSpace(dto.CoverPath)) images.Add(new() { ProviderName = Name, Type = ImageType.Primary, Url = client.Absolute(dto.CoverPath) });
        // Cached JAVBeacon screenshots are the preferred backdrops. The cover
        // is deliberately last so Jellyfin can also offer it as a fallback
        // background without overriding the landscape screenshots.
        images.AddRange(dto.BackdropUrls.Where(x => !string.IsNullOrWhiteSpace(x)).Select(x => new RemoteImageInfo { ProviderName = Name, Type = ImageType.Backdrop, Url = client.Absolute(x) }));
        if (!string.IsNullOrWhiteSpace(dto.CoverPath)) images.Add(new() { ProviderName = Name, Type = ImageType.Backdrop, Url = client.Absolute(dto.CoverPath) });
        return images;
    }

    public Task<HttpResponseMessage> GetImageResponse(string url, CancellationToken ct) => client.GetImage(url, ct);

    private static long? FindReleaseId(BaseItem item)
    {
        if (item.ProviderIds.TryGetValue("JAVBeacon", out var raw) && long.TryParse(raw, CultureInfo.InvariantCulture, out var direct))
            return direct;
        return null;
    }
}

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
    public bool Supports(BaseItem item) => item is Movie && item.ProviderIds.ContainsKey("JAVBeacon");
    public IEnumerable<ImageType> GetSupportedImages(BaseItem item) => [ImageType.Primary, ImageType.Backdrop];

    public async Task<IEnumerable<RemoteImageInfo>> GetImages(BaseItem item, CancellationToken ct)
    {
        if (!item.ProviderIds.TryGetValue("JAVBeacon", out var raw) || !long.TryParse(raw, CultureInfo.InvariantCulture, out var id)) return [];
        var dto = await client.Metadata(id, ct).ConfigureAwait(false);
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
}

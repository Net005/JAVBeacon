using System.Globalization;
using Jellyfin.Plugin.JAVBeacon.Models;
using MediaBrowser.Controller.Library;
using Microsoft.AspNetCore.Authorization;
using Microsoft.AspNetCore.Mvc;

namespace Jellyfin.Plugin.JAVBeacon.Controllers;

[ApiController]
[Authorize]
[Route("JAVBeacon")]
public sealed class JAVBeaconController(ILibraryManager library, JAVBeaconClient client) : ControllerBase
{
    [HttpGet("items/{itemId:guid}/activity")]
    public async Task<ActionResult<ActivityDto>> Activity(Guid itemId, CancellationToken ct)
    {
        if (Plugin.Instance?.Configuration.EnableWebActivity != true) return NotFound();
        var id = ReleaseId(itemId);
        if (id is null) return NotFound();
        return await client.Activity(id.Value, ct).ConfigureAwait(false) is { } value ? Ok(value) : NotFound();
    }

    [HttpPost("items/{itemId:guid}/o")]
    public async Task<ActionResult<ActivityDto>> AddO(Guid itemId, CancellationToken ct)
    {
        if (Plugin.Instance?.Configuration.EnableWebActivity != true) return NotFound();
        var id = ReleaseId(itemId);
        if (id is null) return NotFound();
        return await client.AddO(id.Value, ct).ConfigureAwait(false) is { } value ? Ok(value) : NotFound();
    }

    private long? ReleaseId(Guid itemId)
    {
        var item = library.GetItemById(itemId);
        return item is not null && item.ProviderIds.TryGetValue("JAVBeacon", out var raw) && long.TryParse(raw, CultureInfo.InvariantCulture, out var id) ? id : null;
    }
}
